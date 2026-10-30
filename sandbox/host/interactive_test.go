//go:build linux || darwin

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/capture"
	"github.com/baby-hero-404/aicli/sandbox"
)

func interactiveRuntime(t *testing.T, cmds ...[]string) *Runtime {
	t.Helper()
	return newRuntime(t, func(c *Config) { c.InteractiveCommands = cmds })
}

func interactiveReq(t *testing.T, argv []string) sandbox.CommandRequest {
	t.Helper()
	return sandbox.CommandRequest{TaskID: "login1", Workspace: t.TempDir(), Command: argv, NetworkMode: sandbox.NetworkModeBridge}
}

func TestRunInteractive_ResizeAndOutput(t *testing.T) {
	argv := []string{"sh", "-c", "sleep 0.6; stty size"}
	rt := interactiveRuntime(t, argv)
	req := interactiveReq(t, argv)
	resize := make(chan sandbox.TerminalSize, 1)
	resize <- sandbox.TerminalSize{Rows: 40, Cols: 100}
	req.ResizeCh = resize

	var out bytes.Buffer
	if err := rt.RunInteractive(context.Background(), req, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
	if !strings.Contains(out.String(), "40 100") {
		t.Errorf("terminal size not applied, output = %q", out.String())
	}
	assertRunDirGone(t, rt)
}

func TestRunInteractive_DefaultSizeAndStdin(t *testing.T) {
	argv := []string{"sh", "-c", `stty size; read line; echo got-$line; echo "term=$TERM"`}
	rt := interactiveRuntime(t, argv)
	var out bytes.Buffer
	err := rt.RunInteractive(context.Background(), interactiveReq(t, argv), strings.NewReader("hello\n"), &out, io.Discard)
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
	for _, want := range []string{"24 80", "got-hello", "term=xterm"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %q", want, out.String())
		}
	}
}

func TestRunInteractive_Refused(t *testing.T) {
	allowed := []string{"sh", "-c", "true"}
	rt := interactiveRuntime(t, allowed)
	tests := map[string][]string{
		"bare shell":      {"/bin/bash"},
		"prefix":          {"sh", "-c"},
		"extended":        {"sh", "-c", "true", "extra"},
		"different third": {"sh", "-c", "touch ran"},
	}
	for name, argv := range tests {
		t.Run(name, func(t *testing.T) {
			req := interactiveReq(t, argv)
			err := rt.RunInteractive(context.Background(), req, strings.NewReader(""), io.Discard, io.Discard)
			if !errors.Is(err, ErrNotAllowlisted) {
				t.Fatalf("err = %v, want ErrNotAllowlisted", err)
			}
			if _, statErr := os.Stat(filepath.Join(req.Workspace, "ran")); statErr == nil {
				t.Error("the command ran")
			}
			assertRunDirGone(t, rt)
		})
	}
}

// The pty library makes the child a session leader; setting Setpgid on top of that would fail with
// EPERM. Cancelling must leave neither the shell nor its helper alive.
func TestRunInteractive_CancelLeavesNoProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	argv := []string{"sh", "-c", "sleep 600 & echo $! > " + pidFile + "; wait"}
	rt := interactiveRuntime(t, argv)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if b, err := os.ReadFile(pidFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
				break
			}
		}
		cancel()
	}()
	err := rt.RunInteractive(ctx, interactiveReq(t, argv), strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (a start failure would show EPERM here)", err)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper never started: %v", err)
	}
	var pid int
	for _, c := range strings.TrimSpace(string(b)) {
		pid = pid*10 + int(c-'0')
	}
	waitDead(t, pid)
	assertRunDirGone(t, rt)
}

func TestRunInteractive_UnknownProgram(t *testing.T) {
	argv := []string{"definitely-not-installed-xyz"}
	rt := interactiveRuntime(t, argv)
	err := rt.RunInteractive(context.Background(), interactiveReq(t, argv), strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

type testTerm struct{ out bytes.Buffer }

func (*testTerm) Stdin() io.Reader                    { return strings.NewReader("") }
func (t *testTerm) Stdout() io.Writer                 { return &t.out }
func (*testTerm) Resize() <-chan sandbox.TerminalSize { return nil }

// The same login flow as with the Docker runtime: the session workspace is HOME and the files the
// CLI wrote there come back with the same relative layout.
func TestCaptureSessionOverHostRuntime(t *testing.T) {
	argv := []string{"sh", "-c", `mkdir -p "$CLAUDE_CONFIG_DIR" && echo tok > "$CLAUDE_CONFIG_DIR/.credentials.json"`}
	rt := interactiveRuntime(t, argv)
	s := &capture.Session{Runtime: rt, Command: argv, Profile: "claude_code", WorkspacePrefix: "aicli-host-test"}
	res, err := s.Run(context.Background(), &testTerm{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Files[".claude/.credentials.json"]; got != "tok\n" {
		t.Errorf("Files = %v", res.Files)
	}
	assertRunDirGone(t, rt)
}
