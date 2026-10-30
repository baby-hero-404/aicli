//go:build linux || darwin

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
)

// run.Runner over the host runtime: paths, session state, credentials and capture all work with
// the runtime's layout instead of the container's.
func TestRunnerOverHostRuntime(t *testing.T) {
	rt := newRuntime(t, func(c *Config) { c.AllowTools = true }) // the stand-in CLI is bash, not a notools profile
	ws, session := t.TempDir(), t.TempDir()
	runner := &run.Runner{Runtime: rt}

	res, err := runner.Run(context.Background(), run.Request{
		Profile: "claude_code", Command: "bash", TaskID: "turn1",
		Args: []string{"-c", `echo "prompt=$(cat {prompt_file})"; echo "pwd=$(pwd)"; echo "home=$HOME cfg=$CLAUDE_CONFIG_DIR ctx=$(cat $AICLI_CONTEXT_DIR/a.md)"
			echo kept > "$CLAUDE_CONFIG_DIR/state"; echo refreshed > "$CLAUDE_CONFIG_DIR/.credentials.json"; echo result > {workdir}/out.txt`},
		Prompt: "hello", Workspace: ws, SessionDir: session,
		CredentialFiles: map[string]string{".claude/.credentials.json": "tok"},
		ContextFiles:    map[string]string{"a.md": "ctx-data"},
		CaptureFiles:    []string{"out.txt"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	realWS, _ := filepath.EvalSymlinks(ws)
	home := filepath.Join(rt.cfg.RunRoot, "turn1", "home")
	if !res.Success {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"prompt=hello", "pwd=" + realWS, "home=" + home + " cfg=" + home + "/.claude ctx=ctx-data"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
	if res.Files["out.txt"] != "result\n" {
		t.Errorf("captured files = %v", res.Files)
	}
	if b, err := os.ReadFile(filepath.Join(session, ".claude", "state")); err != nil || string(b) != "kept\n" {
		t.Errorf("session state = %q, %v", b, err)
	}
	// Credentials were handed in as a plain file and the refreshed one is reported by HOME-relative path.
	if got := res.UpdatedCredentialFiles[".claude/.credentials.json"]; got != "refreshed\n" {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
	if _, err := os.Stat(filepath.Join(ws, ".aicli")); !os.IsNotExist(err) {
		t.Errorf("the state directory should be removed from the workspace, stat err = %v", err)
	}
	assertRunDirGone(t, rt)

	warn, err := runner.Preflight(context.Background(), run.Request{
		Profile: "claude_code", Command: "bash", TaskID: "turn1", Workspace: ws,
		AuthCheckCommand: `echo '{"loggedIn": true}'`, Env: map[string]string{"X": "1"},
	})
	if err != nil || warn != "" {
		t.Errorf("Preflight = %q, %v", warn, err)
	}
}

// The tool guardrail through the real run.Runner: the registry's own invocation of a notools
// profile passes it, anything that alters the invocation does not.
func TestRunnerGuardrail(t *testing.T) {
	p, _ := profile.Lookup("claude_code_notools")
	marker := filepath.Join(t.TempDir(), "setup-ran")
	base := func() run.Request {
		return run.Request{Profile: "claude_code_notools", Command: p.Command, Args: p.Args, Prompt: "p", Workspace: t.TempDir(), TaskID: "g1"}
	}
	rt := newRuntime(t)
	runner := &run.Runner{Runtime: rt}

	// The CLI is not installed here, so the shell reports 127 — which shows the guardrail let it start.
	res, err := runner.Run(context.Background(), base())
	if err != nil || res.ExitCode != 127 {
		t.Fatalf("registry invocation: %+v, %v", res, err)
	}

	for name, mutate := range map[string]func(*run.Request){
		"extra args":      func(r *run.Request) { r.ExtraArgs = []string{"--allowedTools", "Bash"} },
		"setup snippet":   func(r *run.Request) { r.Setup = "touch " + marker },
		"changed args":    func(r *run.Request) { r.Args = []string{"--allowedTools", "Bash", "-p", "x"} },
		"another profile": func(r *run.Request) { r.Profile = "claude_code" },
	} {
		t.Run(name, func(t *testing.T) {
			req := base()
			mutate(&req)
			if _, err := runner.Run(context.Background(), req); !errors.Is(err, ErrToolsNotAllowed) {
				t.Errorf("err = %v, want ErrToolsNotAllowed", err)
			}
		})
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a refused setup snippet ran")
	}
}

func BenchmarkHostRun(b *testing.B) {
	cfg := Config{RunRoot: b.TempDir()}
	rt, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	ws := b.TempDir()
	req := sandbox.CommandRequest{TaskID: "bench", Workspace: ws, Command: []string{"true"}, Profile: "claude_code_notools"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rt.Run(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
