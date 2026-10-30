package toolchain

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
)

// TestDockerMountedTools needs Docker and an already populated tools root:
//
//	aicli-tools --root DIR setup
//	AICLI_DOCKER_TESTS=1 AICLI_TOOLS_ROOT=DIR go test ./toolchain -run Docker
func TestDockerMountedTools(t *testing.T) {
	root := os.Getenv("AICLI_TOOLS_ROOT")
	if os.Getenv("AICLI_DOCKER_TESTS") != "1" || root == "" {
		t.Skip("set AICLI_DOCKER_TESTS=1 and AICLI_TOOLS_ROOT to a populated tools root")
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := dockerrt.New(dockerrt.Config{Image: os.Getenv("AICLI_TEST_IMAGE")})
	if err != nil {
		t.Fatal(err)
	}
	rt := Wrap(inner, store)
	ctx := context.Background()

	script := `for t in agy claude codex; do printf '%s=' "$t"; HOME=$(mktemp -d) $t --version 2>&1 | tail -1; printf '%s-path=%s\n' "$t" "$(command -v $t)"; done
touch /opt/aicli/tools/agy/x 2>/dev/null && echo WRITABLE || echo READONLY
printf 'env-agy=%s env-claude=%s\n' "$AGY_CLI_DISABLE_AUTO_UPDATE" "$DISABLE_AUTOUPDATER"`
	// bash -l resets PATH from /etc/profile; the mounted profile.d script must restore it.
	res, err := rt.Run(ctx, sandbox.CommandRequest{Command: []string{"bash", "-lc", script}, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Stdout + res.Stderr
	t.Log(out)
	for _, tool := range Tools() {
		cur, _ := store.Current(tool)
		if cur == "" {
			continue
		}
		if !strings.Contains(out, tool+"-path=/opt/aicli/tools/"+tool+"/bin/") {
			t.Errorf("%s does not come from the mount in a login shell", tool)
		}
		if line := lineWith(out, tool+"="); !strings.Contains(line, cur) {
			t.Errorf("%s reports %q, current is %s", tool, line, cur)
		}
	}
	for _, want := range []string{"READONLY", "env-agy=1 env-claude=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}

	// run.Runner.Preflight finds the mounted CLI through the same wrapper.
	r := &run.Runner{Runtime: rt}
	if _, err := r.Preflight(ctx, run.Request{Profile: "antigravity", Command: "agy", Workspace: t.TempDir()}); err != nil {
		t.Errorf("Preflight: %v", err)
	}
}

func lineWith(s, prefix string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
