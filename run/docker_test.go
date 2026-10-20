package run

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
)

// TestRunner_DockerRoundTrip runs a stand-in "CLI" (bash) in a real container to verify the
// generated script, prompt file, capture blocks and cleanup end to end. Opt in with
// AICLI_DOCKER_TESTS=1 and pick the image with AICLI_TEST_IMAGE.
func TestRunner_DockerRoundTrip(t *testing.T) {
	if os.Getenv("AICLI_DOCKER_TESTS") != "1" {
		t.Skip("set AICLI_DOCKER_TESTS=1 to run Docker integration tests")
	}
	rt, err := dockerrt.New(dockerrt.Config{Image: os.Getenv("AICLI_TEST_IMAGE")})
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("prewarm: %v", err)
	}

	ws := t.TempDir()
	res, err := (&Runner{Runtime: rt}).Run(ctx, Request{
		Command:      "bash",
		Args:         []string{"-c", `echo "prompt=$(cat {prompt_file})"; echo result > {workdir}/out.txt; echo "home=$HOME ci=$CI"`},
		Prompt:       "hello from aicli",
		Workspace:    ws,
		CaptureFiles: []string{"out.txt"},
		Timeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("not successful: %+v", res)
	}
	for _, want := range []string{"prompt=hello from aicli", "home=" + sandbox.HomeDir, "ci=1"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
	if strings.TrimSpace(res.Files["out.txt"]) != "result" {
		t.Errorf("captured files = %v", res.Files)
	}
	if _, err := os.Stat(ws + "/.aicli"); !os.IsNotExist(err) {
		t.Errorf("state dir should be gone, stat err = %v", err)
	}
}

// TestRunner_DockerSessionStatePerAccount checks the multi-account contract end to end: state a
// CLI writes under its StateDir persists to the next run of the same account and is invisible
// to another account.
func TestRunner_DockerSessionStatePerAccount(t *testing.T) {
	if os.Getenv("AICLI_DOCKER_TESTS") != "1" {
		t.Skip("set AICLI_DOCKER_TESTS=1 to run Docker integration tests")
	}
	rt, err := dockerrt.New(dockerrt.Config{Image: os.Getenv("AICLI_TEST_IMAGE")})
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runner := &Runner{Runtime: rt}
	root := t.TempDir()
	dirA, _ := sandbox.SessionDir(root, "claude_code", "acct-a")
	dirB, _ := sandbox.SessionDir(root, "claude_code", "acct-b")

	// Stand-in for the CLI: it reads and writes state where claude would ($CLAUDE_CONFIG_DIR).
	runAs := func(dir, script string) string {
		res, err := runner.Run(ctx, Request{
			Profile: "claude_code", Command: "bash", Args: []string{"-c", script},
			Workspace: t.TempDir(), SessionDir: dir, Timeout: 30 * time.Second,
		})
		if err != nil || !res.Success {
			t.Fatalf("run: %+v, %v", res, err)
		}
		return res.Output
	}
	runAs(dirA, `echo token-a > "$CLAUDE_CONFIG_DIR/state"`)
	if out := runAs(dirA, `cat "$CLAUDE_CONFIG_DIR/state"`); !strings.Contains(out, "token-a") {
		t.Errorf("account A lost its state between runs: %q", out)
	}
	if out := runAs(dirB, `cat "$CLAUDE_CONFIG_DIR/state" 2>/dev/null || echo none`); strings.Contains(out, "token-a") {
		t.Errorf("account B saw account A's state: %q", out)
	}
}
