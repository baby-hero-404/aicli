// Package conformance holds the behavior every sandbox.Runtime implementation must share, as a
// test suite driven only through the Runtime interface and sandbox.LayoutOf. Runtimes call Run
// from their own tests so that sandbox/docker and sandbox/host cannot drift apart.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// Options adapts the suite to the runtime under test.
type Options struct {
	// Prepare, if set, adjusts every request before it is run, for example to set
	// sandbox.CommandRequest.Profile for a runtime that gates on it.
	Prepare func(*sandbox.CommandRequest)
}

// Run executes the suite against rt. Commands are POSIX sh scripts, so the runtime's environment
// must provide sh.
func Run(t *testing.T, rt sandbox.Runtime, opts Options) {
	t.Helper()
	s := &suite{rt: rt, opts: opts}
	for _, c := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"exit code and output streams", s.exitCodeAndStreams},
		{"workspace is the working directory", s.workspaceIsCwd},
		{"environment reaches the process", s.environment},
		{"live output and log file", s.liveOutputAndLogFile},
		{"credential files are written back when changed", s.credentialWriteBack},
		{"unchanged credential files are not reported", s.credentialUnchanged},
		{"session mount persists what the process writes", s.sessionMount},
		{"timeout", s.timeout},
		{"context cancel", s.cancel},
		{"idle process is killed", s.idleKill},
		{"repeating output is killed", s.loopKill},
		{"active process is not killed", s.activeNotKilled},
	} {
		t.Run(c.name, c.fn)
	}
}

type suite struct {
	rt   sandbox.Runtime
	opts Options
	n    int
}

// request returns a request for script with a fresh task ID and a workspace the sandbox user can
// write to, plus its layout.
func (s *suite) request(t *testing.T, script string) (sandbox.CommandRequest, sandbox.Layout) {
	t.Helper()
	s.n++
	ws := s.dir(t)
	req := sandbox.CommandRequest{
		TaskID: fmt.Sprintf("conf-%d-%d", os.Getpid(), s.n), Workspace: ws,
		Command: []string{"sh", "-c", script}, NetworkMode: sandbox.NetworkModeBridge,
	}
	if s.opts.Prepare != nil {
		s.opts.Prepare(&req)
	}
	layout, err := sandbox.LayoutOf(s.rt, req)
	if err != nil {
		t.Fatalf("LayoutOf: %v", err)
	}
	req.Env = map[string]string{"HOME": layout.Home}
	return req, layout
}

// dir returns an empty directory any user can write to (a container's user is not the test's),
// removed best-effort because a container may have created root-owned files in it.
func (s *suite) dir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "aicli-conformance-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func (s *suite) run(t *testing.T, req sandbox.CommandRequest) *sandbox.CommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := s.rt.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func (s *suite) exitCodeAndStreams(t *testing.T) {
	req, _ := s.request(t, `echo out; echo err >&2; exit 3`)
	res := s.run(t, req)
	if res.ExitCode != 3 || res.Stdout != "out" || res.Stderr != "err" || res.Killed {
		t.Errorf("result = %+v", res)
	}
}

func (s *suite) workspaceIsCwd(t *testing.T) {
	req, _ := s.request(t, `echo data > conformance.txt`)
	if res := s.run(t, req); res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(req.Workspace, "conformance.txt")); err != nil || string(b) != "data\n" {
		t.Errorf("workspace file = %q, %v", b, err)
	}
}

func (s *suite) environment(t *testing.T) {
	req, layout := s.request(t, `printf '%s|%s' "$FOO" "$HOME"`)
	req.Env["FOO"] = "bar"
	if res := s.run(t, req); res.Stdout != "bar|"+layout.Home {
		t.Errorf("stdout = %q, want %q", res.Stdout, "bar|"+layout.Home)
	}
}

func (s *suite) liveOutputAndLogFile(t *testing.T) {
	req, _ := s.request(t, `echo to-out; echo to-err >&2`)
	var live bytes.Buffer
	req.Output = &live
	req.LogFilePath = filepath.Join(s.dir(t), "logs", "run.log")
	s.run(t, req)
	if got := live.String(); !strings.Contains(got, "to-out") || strings.Contains(got, "to-err") {
		t.Errorf("Output received %q, want stdout only", got)
	}
	log, err := os.ReadFile(req.LogFilePath)
	if err != nil || !strings.Contains(string(log), "to-out") || !strings.Contains(string(log), "to-err") {
		t.Errorf("log file = %q, %v", log, err)
	}
}

func (s *suite) credentialWriteBack(t *testing.T) {
	req, layout := s.request(t, `cat "$HOME/.conf/cred.json"; echo refreshed > "$HOME/.conf/cred.json"`)
	target := layout.Home + "/.conf/cred.json"
	req.CredentialFiles = map[string]string{target: "token-1"}
	res := s.run(t, req)
	if res.Stdout != "token-1" {
		t.Errorf("the process saw %q, want the credential content", res.Stdout)
	}
	if len(res.UpdatedCredentialFiles) != 1 || res.UpdatedCredentialFiles[target] != "refreshed\n" {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
}

func (s *suite) credentialUnchanged(t *testing.T) {
	req, layout := s.request(t, `cat "$HOME/.conf/cred.json" > /dev/null`)
	req.CredentialFiles = map[string]string{layout.Home + "/.conf/cred.json": "token-1"}
	if res := s.run(t, req); len(res.UpdatedCredentialFiles) != 0 {
		t.Errorf("UpdatedCredentialFiles = %v, want none", res.UpdatedCredentialFiles)
	}
}

func (s *suite) sessionMount(t *testing.T) {
	session := s.dir(t)
	req, layout := s.request(t, `echo kept > "$HOME/.state/f"`)
	req.SessionMounts = map[string]string{layout.Home + "/.state": session}
	if res := s.run(t, req); res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(session, "f")); err != nil || string(b) != "kept\n" {
		t.Errorf("session file = %q, %v", b, err)
	}
}

func (s *suite) timeout(t *testing.T) {
	req, _ := s.request(t, `sleep 60`)
	req.Timeout = 500 * time.Millisecond
	start := time.Now()
	_, err := s.rt.Run(context.Background(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v", d)
	}
}

func (s *suite) cancel(t *testing.T) {
	req, _ := s.request(t, `sleep 60`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	if _, err := s.rt.Run(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func (s *suite) expectKilled(t *testing.T, res *sandbox.CommandResult, reason string) {
	t.Helper()
	if !res.Killed || res.KillReason != reason || res.ExitCode != 137 {
		t.Errorf("Killed = %v reason = %q exit = %d, want killed by %q with exit 137", res.Killed, res.KillReason, res.ExitCode, reason)
	}
}

func (s *suite) idleKill(t *testing.T) {
	req, _ := s.request(t, `echo started; sleep 60`)
	req.IdleTimeout = time.Second
	s.expectKilled(t, s.run(t, req), sandbox.KillReasonIdleTimeout)
}

func (s *suite) loopKill(t *testing.T) {
	req, _ := s.request(t, `i=0; while [ $i -lt 40 ]; do echo same line; i=$((i+1)); done; sleep 60`)
	s.expectKilled(t, s.run(t, req), sandbox.KillReasonLoopDetected)
}

func (s *suite) activeNotKilled(t *testing.T) {
	req, _ := s.request(t, `for i in 1 2 3 4 5 6 7 8; do echo "line $i"; sleep 0.5; done`)
	req.IdleTimeout = 2 * time.Second
	if res := s.run(t, req); res.Killed || res.ExitCode != 0 {
		t.Errorf("result = %+v", res)
	}
}
