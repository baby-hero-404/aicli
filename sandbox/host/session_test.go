//go:build linux || darwin

package host

import (
	"bufio"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// readLine reads one line from r with a timeout, failing the test if none arrives.
func readLine(t *testing.T, r *bufio.Reader, timeout time.Duration) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		l, err := r.ReadString('\n')
		ch <- result{l, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read line: %v", res.err)
		}
		return strings.TrimRight(res.line, "\n")
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a line")
		return ""
	}
}

// TestStart_TurnsRoundTrip pins the one property the whole warm pool depends on: writing a line
// to Stdin and reading it back from Stdout over plain pipes, with no PTY artifacts (no local
// echo, no "\n" rewritten to "\r\n" — both of which RunInteractive's PTY would introduce and
// which would corrupt a newline-delimited-JSON protocol).
func TestStart_TurnsRoundTrip(t *testing.T) {
	rt := newRuntime(t)
	req := shReq(t, `while IFS= read -r line; do printf 'got:%s\n' "$line"; done`)
	p, err := rt.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := bufio.NewReader(p.Stdout())

	for _, line := range []string{"first", `{"n":1}`, "third"} {
		if _, err := p.Stdin().Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := readLine(t, out, 5*time.Second)
		if got != "got:"+line {
			t.Errorf("got %q, want %q (a PTY would have added \\r or echoed the input line)", got, "got:"+line)
		}
	}
	if err := p.Stdin().Close(); err != nil {
		t.Errorf("close stdin: %v", err)
	}
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
}

// TestStart_CredentialIsolation: two sessions started with different credential content get
// different HOME directories and never see each other's file — the same guarantee Run gives a
// one-shot command, which the warm pool's "never share a process across accounts" rule depends
// on (docs/openspecs/ai-cli-warm-pool/specs.md Invariants).
func TestStart_CredentialIsolation(t *testing.T) {
	rt := newRuntime(t)
	reqA := sandbox.CommandRequest{
		TaskID: "account-a", Workspace: t.TempDir(), Profile: "claude_code_notools",
		// Echoing a bare newline after cat guarantees a line terminator even though the
		// credential content below has none of its own: without it, a correct Session still
		// produces output ("secret-a" with no trailing "\n"), it is this test's own
		// line-oriented read that would hang waiting for a "\n" that normal CLI output
		// (newline-delimited JSON per Phase 0) always includes.
		Command:         []string{"sh", "-c", `while IFS= read -r line; do cat "$HOME/.claude.json" 2>/dev/null || echo none; echo; done`},
		CredentialFiles: map[string]string{filepath.Join(rt.cfg.RunRoot, "account-a", "home", ".claude.json"): "secret-a"},
	}
	reqB := reqA
	reqB.TaskID = "account-b"
	reqB.CredentialFiles = map[string]string{filepath.Join(rt.cfg.RunRoot, "account-b", "home", ".claude.json"): "secret-b"}

	pA, err := rt.Start(context.Background(), reqA)
	if err != nil {
		t.Fatalf("start A: %v", err)
	}
	pB, err := rt.Start(context.Background(), reqB)
	if err != nil {
		t.Fatalf("start B: %v", err)
	}
	outA, outB := bufio.NewReader(pA.Stdout()), bufio.NewReader(pB.Stdout())

	pA.Stdin().Write([]byte("x\n"))
	pB.Stdin().Write([]byte("x\n"))
	if got := readLine(t, outA, 5*time.Second); got != "secret-a" {
		t.Errorf("account A saw %q", got)
	}
	if got := readLine(t, outB, 5*time.Second); got != "secret-b" {
		t.Errorf("account B saw %q", got)
	}
	pA.Stdin().Close()
	pB.Stdin().Close()
	pA.Wait()
	pB.Wait()
}

// TestStart_KillLeavesNoOrphan: Kill ends the whole process group, including a child the session
// spawned, matching Run's own guarantee (killGroup) that a helper never outlives the command.
func TestStart_KillLeavesNoOrphan(t *testing.T) {
	rt := newRuntime(t)
	req := shReq(t, `sh -c 'sleep 100' & wait`)
	p, err := rt.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	hp := p.(*process)
	pid := hp.cmd.Process.Pid
	// give the grandchild time to actually start before killing the group
	time.Sleep(100 * time.Millisecond)
	if err := p.Kill("test"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.Killed || res.KillReason != "test" {
		t.Errorf("result = %+v", res)
	}
	// the process group leader and everything in it must be gone; signalling an empty/gone
	// group returns ESRCH, which killGroup already treats as success, so the real assertion is
	// that no process in that pgid remains schedulable.
	dead := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(-pid, 0); err != nil {
			dead = true
			break
		}
	}
	if !dead {
		t.Error("process group still has members after Kill")
	}
}

// TestStart_WaitTwiceErrors: Wait must be called exactly once; a second call must not panic
// (os/exec's own Wait panics on a double call) or race the first.
func TestStart_WaitTwiceErrors(t *testing.T) {
	rt := newRuntime(t)
	p, err := rt.Start(context.Background(), shReq(t, `true`))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if _, err := p.Wait(); err == nil {
		t.Error("second Wait must return an error, not panic or block")
	}
}

// TestStart_UpdatedCredentialFiles: a session that rewrites its credential file during the
// turns it serves reports the new content on Wait, the same as Run does at the end of a one-shot
// command — login.finish() in the faster-learning provider depends on this to persist a
// refreshed token exactly once, after the session closes.
func TestStart_UpdatedCredentialFiles(t *testing.T) {
	rt := newRuntime(t)
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	req := shReq(t, `while IFS= read -r line; do echo refreshed > "$HOME/.claude.json"; echo ok; done`)
	req.CredentialFiles = map[string]string{home + "/.claude.json": "token-1"}
	p, err := rt.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := bufio.NewReader(p.Stdout())
	p.Stdin().Write([]byte("turn\n"))
	readLine(t, out, 5*time.Second)
	p.Stdin().Close()
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.UpdatedCredentialFiles[home+"/.claude.json"] != "refreshed\n" {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
}

// TestStart_RunDirRemovedAfterWait: the private run directory (HOME, credential files) is
// cleaned up once Wait returns, same as after Run — a warm session must not leak a HOME per
// turn.
func TestStart_RunDirRemovedAfterWait(t *testing.T) {
	rt := newRuntime(t)
	p, err := rt.Start(context.Background(), shReq(t, `true`))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	assertRunDirGone(t, rt)
}

// TestStart_ContextCancelledBeforeStart: a context already done when Start is called must not
// leave a process running or a run directory behind.
func TestStart_ContextCancelledBeforeStart(t *testing.T) {
	rt := newRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rt.Start(ctx, shReq(t, `true`)); err == nil {
		t.Error("Start with an already-cancelled context must fail")
	}
	assertRunDirGone(t, rt)
}

// TestStart_RefusesSameTaskID: Start shares the one-shot path's per-task-ID guard (Run also
// refuses a reused TaskID while one is in progress), so a caller cannot accidentally start two
// warm sessions under the same run directory.
func TestStart_RefusesSameTaskID(t *testing.T) {
	rt := newRuntime(t)
	req := shReq(t, `while IFS= read -r line; do :; done`)
	p, err := rt.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Start(context.Background(), req); err == nil {
		t.Error("starting a second session with the same TaskID must fail")
	}
	p.Stdin().Close()
	p.Wait()
}

// TestStart_ConcurrentSessions exercises several sessions started and finished concurrently, to
// catch a data race in the per-task-ID bookkeeping Start shares with Run (go test -race).
func TestStart_ConcurrentSessions(t *testing.T) {
	rt := newRuntime(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := sandbox.CommandRequest{
				TaskID: "concurrent-" + string(rune('a'+i)), Workspace: t.TempDir(), Profile: "claude_code_notools",
				Command: []string{"sh", "-c", `while IFS= read -r line; do echo "$line"; done`},
			}
			p, err := rt.Start(context.Background(), req)
			if err != nil {
				t.Errorf("Start: %v", err)
				return
			}
			out := bufio.NewReader(p.Stdout())
			p.Stdin().Write([]byte("hi\n"))
			readLine(t, out, 5*time.Second)
			p.Stdin().Close()
			if _, err := p.Wait(); err != nil {
				t.Errorf("Wait: %v", err)
			}
		}()
	}
	wg.Wait()
	assertRunDirGone(t, rt)
}
