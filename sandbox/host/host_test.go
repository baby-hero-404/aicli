//go:build linux || darwin

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/internal/conformance"
)

// newRuntime returns a runtime whose watchdog polls quickly.
func newRuntime(t *testing.T, mutate ...func(*Config)) *Runtime {
	t.Helper()
	cfg := testCfg(t)
	for _, m := range mutate {
		m(&cfg)
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt.stallEvery = 10 * time.Millisecond
	return rt
}

func shReq(t *testing.T, script string) sandbox.CommandRequest {
	t.Helper()
	req := okReq(t)
	req.Command = []string{"sh", "-c", script}
	return req
}

func mustRun(t *testing.T, rt *Runtime, req sandbox.CommandRequest) *sandbox.CommandResult {
	t.Helper()
	res, err := rt.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func assertRunDirGone(t *testing.T, rt *Runtime) {
	t.Helper()
	entries, err := os.ReadDir(rt.cfg.RunRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("run root still holds %d entries, first: %s", len(entries), entries[0].Name())
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return
		}
	}
	t.Errorf("process %d is still alive", pid)
}

func TestRun_OutputExitAndEnvironment(t *testing.T) {
	t.Setenv("AICLI_HOST_TEST_SECRET", "leak")
	rt := newRuntime(t)
	req := shReq(t, `pwd; echo "home=$HOME tmp=$TMPDIR"; echo out-$FOO; echo err >&2; env | sort; exit 3`)
	req.Env = map[string]string{"FOO": "bar"}
	res := mustRun(t, rt, req)

	if res.ExitCode != 3 || res.Killed {
		t.Errorf("exit = %d killed = %v", res.ExitCode, res.Killed)
	}
	if res.Stderr != "err" {
		t.Errorf("stderr = %q", res.Stderr)
	}
	real, _ := filepath.EvalSymlinks(req.Workspace)
	layout, _ := rt.Layout(req)
	for _, want := range []string{real + "\n", "home=" + layout.Home + " tmp=" + filepath.Join(rt.cfg.RunRoot, "t1", "tmp"), "out-bar"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "AICLI_HOST_TEST_SECRET") || strings.Contains(res.Stdout, "leak") {
		t.Errorf("the server's environment leaked into the child:\n%s", res.Stdout)
	}
	assertRunDirGone(t, rt)
}

func TestRun_SignalExitCode(t *testing.T) {
	res := mustRun(t, newRuntime(t), shReq(t, `kill -9 $$`))
	if res.ExitCode != 137 {
		t.Errorf("exit = %d, want 137 like a container", res.ExitCode)
	}
}

func TestRun_Refused(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sandbox.CommandRequest)
		want   error
	}{
		{"no profile", func(r *sandbox.CommandRequest) { r.Profile = "" }, ErrToolsNotAllowed},
		{"profile with tools", func(r *sandbox.CommandRequest) { r.Profile = "openai_codex" }, ErrToolsNotAllowed},
		{"network none", func(r *sandbox.CommandRequest) { r.NetworkMode = sandbox.NetworkModeNone }, ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newRuntime(t)
			req := shReq(t, `touch "$PWD/ran"`)
			tt.mutate(&req)
			_, err := rt.Run(context.Background(), req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(req.Workspace, "ran")); statErr == nil {
				t.Error("the command ran although the request was refused")
			}
			assertRunDirGone(t, rt)
		})
	}
}

func TestRun_AllowToolsRunsAnyProfile(t *testing.T) {
	rt := newRuntime(t, func(c *Config) { c.AllowTools = true })
	req := shReq(t, `echo hi`)
	req.Profile = ""
	if res := mustRun(t, rt, req); res.Stdout != "hi" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestRun_CommandNotFound(t *testing.T) {
	rt := newRuntime(t)
	req := okReq(t)
	req.Command = []string{"definitely-not-installed-xyz"}
	if _, err := rt.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
	req.Command = []string{"sh", "-c", "definitely-not-installed-xyz"}
	if res := mustRun(t, rt, req); res.ExitCode != 127 {
		t.Errorf("exit = %d, want 127 from the shell", res.ExitCode)
	}
	assertRunDirGone(t, rt)
}

func TestRun_SessionStateAndCredentials(t *testing.T) {
	rt := newRuntime(t)
	session := t.TempDir()
	req := shReq(t, `
		echo kept > "$HOME/.claude/state"
		test "$(ls -ld "$HOME/.claude.json" | cut -c1-10)" = "-rw-------" || echo "bad credential mode"
		cat "$HOME/.claude.json"
		echo refreshed > "$HOME/.claude.json"
		echo same-content > "$HOME/.codex.json"`)
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	req.SessionMounts = map[string]string{home + "/.claude": session}
	req.CredentialFiles = map[string]string{home + "/.claude.json": "token-1", home + "/.codex.json": "same-content\n"}
	res := mustRun(t, rt, req)

	if res.ExitCode != 0 || strings.Contains(res.Stdout, "bad credential mode") || !strings.Contains(res.Stdout, "token-1") {
		t.Errorf("result = %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(session, "state")); err != nil || string(b) != "kept\n" {
		t.Errorf("session state = %q, %v", b, err)
	}
	// Only the file whose content changed is reported.
	if len(res.UpdatedCredentialFiles) != 1 || res.UpdatedCredentialFiles[home+"/.claude.json"] != "refreshed\n" {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
	assertRunDirGone(t, rt)
	if _, err := os.Stat(session); err != nil {
		t.Errorf("removing the run directory must not touch the session directory: %v", err)
	}
	if entries, _ := os.ReadDir(session); len(entries) != 1 {
		t.Errorf("session directory holds %d entries, want only the CLI's own file", len(entries))
	}
}

func TestRun_UnchangedCredentialsAreNotReported(t *testing.T) {
	rt := newRuntime(t)
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	req := shReq(t, `true`)
	req.CredentialFiles = map[string]string{home + "/.c": "x"}
	if res := mustRun(t, rt, req); res.UpdatedCredentialFiles != nil {
		t.Errorf("UpdatedCredentialFiles = %v, want nil", res.UpdatedCredentialFiles)
	}
}

// The usual layout: credentials live inside the CLI's session-mounted state directory. They are
// written into the session directory (as the Docker runtime does), replacing — never following —
// anything already at that path.
func TestRun_CredentialInsideSessionMount(t *testing.T) {
	hostFile := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(hostFile, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := t.TempDir()
	// A previous run (or the CLI) left a symlink where the credential goes.
	if err := os.Symlink(hostFile, filepath.Join(session, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(t)
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	target := home + "/.claude/.credentials.json"
	req := shReq(t, `cat "$HOME/.claude/.credentials.json"; echo refreshed > "$HOME/.claude/.credentials.json"`)
	req.SessionMounts = map[string]string{home + "/.claude": session}
	req.CredentialFiles = map[string]string{target: "tok"}
	res := mustRun(t, rt, req)

	if res.Stdout != "tok" || res.UpdatedCredentialFiles[target] != "refreshed\n" {
		t.Errorf("result = %+v", res)
	}
	if b, _ := os.ReadFile(hostFile); string(b) != "untouched" {
		t.Errorf("the write followed a symlink out of the session directory: %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(session, ".credentials.json")); err != nil || string(b) != "refreshed\n" {
		t.Errorf("session credential = %q, %v", b, err)
	}
	assertRunDirGone(t, rt)
}

func TestRun_CredentialReplacedBySymlinkIsIgnored(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(t)
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	req := shReq(t, `rm "$HOME/.c" && ln -s `+secret+` "$HOME/.c"`)
	req.CredentialFiles = map[string]string{home + "/.c": "x"}
	if res := mustRun(t, rt, req); res.UpdatedCredentialFiles != nil {
		t.Errorf("a symlinked credential file leaked: %v", res.UpdatedCredentialFiles)
	}
}

func TestRun_SessionLinkReplaced(t *testing.T) {
	rt := newRuntime(t)
	session := t.TempDir()
	home := filepath.Join(rt.cfg.RunRoot, "t1", "home")
	tests := []struct {
		name, script string
		wantErr      bool
	}{
		{"written into", `echo x > "$HOME/.claude/f"`, false},
		{"replaced by a directory", `rm "$HOME/.claude" && mkdir "$HOME/.claude" && echo x > "$HOME/.claude/f"`, true},
		{"removed", `rm "$HOME/.claude"`, true},
		{"repointed", `rm "$HOME/.claude" && ln -s /tmp "$HOME/.claude"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := shReq(t, tt.script)
			req.SessionMounts = map[string]string{home + "/.claude": session}
			_, err := rt.Run(context.Background(), req)
			if got := errors.Is(err, ErrSessionDetached); got != tt.wantErr || (!tt.wantErr && err != nil) {
				t.Errorf("err = %v, want detached = %v", err, tt.wantErr)
			}
			assertRunDirGone(t, rt)
		})
	}
}

func TestRun_MountSources(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	allowed := t.TempDir()
	outside := t.TempDir()
	linkToHome := filepath.Join(allowed, "to-home")
	if err := os.Symlink(fakeHome, linkToHome); err != nil {
		t.Fatal(err)
	}
	linkOut := filepath.Join(allowed, "to-outside")
	if err := os.Symlink(outside, linkOut); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		allowed []string
		session func() string
		ws      func() string
		wantErr string
	}{
		{"session symlink to HOME", nil, func() string { return linkToHome }, nil, "HOME"},
		{"session symlink out of allowed roots", []string{allowed}, func() string { return linkOut }, nil, "allowed roots"},
		{"session inside allowed roots", []string{allowed}, func() string { return filepath.Join(allowed, "s") }, func() string { return filepath.Join(allowed, "ws") }, ""},
		{"workspace outside allowed roots", []string{allowed}, nil, func() string { return outside }, "allowed roots"},
		{"workspace is a file", nil, nil, func() string {
			p := filepath.Join(outside, "file")
			_ = os.WriteFile(p, nil, 0o600)
			return p
		}, "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newRuntime(t, func(c *Config) { c.AllowedRoots = tt.allowed })
			req := shReq(t, `true`)
			if tt.ws != nil {
				req.Workspace = tt.ws()
			}
			if tt.session != nil {
				req.SessionMounts = map[string]string{filepath.Join(rt.cfg.RunRoot, "t1", "home", ".claude"): tt.session()}
			}
			_, err := rt.Run(context.Background(), req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			assertRunDirGone(t, rt)
		})
	}
}

func TestRun_WorkspaceCreatedFromRoot(t *testing.T) {
	root := t.TempDir()
	rt := newRuntime(t, func(c *Config) { c.WorkspaceRoot = root })
	req := shReq(t, `echo data > f`)
	req.Workspace = ""
	mustRun(t, rt, req)
	if b, err := os.ReadFile(filepath.Join(root, "t1", "f")); err != nil || string(b) != "data\n" {
		t.Errorf("workspace file = %q, %v", b, err)
	}
}

func TestRun_TaskAlreadyRunning(t *testing.T) {
	rt := newRuntime(t)
	started := make(chan struct{})
	first := shReq(t, `sleep 5`)
	first.IdleTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := rt.Run(ctx, first)
		done <- err
	}()
	<-started
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(rt.cfg.RunRoot, "t1")); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("first run did not start")
		}
	}
	if _, err := rt.Run(context.Background(), shReq(t, `true`)); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second run with the same task id: err = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("first run: err = %v", err)
	}
	assertRunDirGone(t, rt)
}

func TestRun_TimeoutAndCancelKillTheGroup(t *testing.T) {
	// The script leaves a background helper in the same process group and records its pid.
	script := func(pidFile string) string {
		return `sleep 600 & echo $! > ` + pidFile + `; wait`
	}
	readPID := func(t *testing.T, file string) int {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if b, err := os.ReadFile(file); err == nil && len(bytes.TrimSpace(b)) > 0 {
				pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				return pid
			}
		}
		t.Fatal("helper pid not recorded")
		return 0
	}
	t.Run("timeout", func(t *testing.T) {
		rt := newRuntime(t)
		pidFile := filepath.Join(t.TempDir(), "pid")
		req := shReq(t, script(pidFile))
		req.Timeout = 300 * time.Millisecond
		start := time.Now()
		_, err := rt.Run(context.Background(), req)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
		waitDead(t, readPID(t, pidFile))
		assertRunDirGone(t, rt)
	})
	t.Run("cancel", func(t *testing.T) {
		rt := newRuntime(t)
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithCancel(context.Background())
		go func() { readPID(t, pidFile); cancel() }()
		if _, err := rt.Run(ctx, shReq(t, script(pidFile))); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
		waitDead(t, readPID(t, pidFile))
		assertRunDirGone(t, rt)
	})
	t.Run("normal exit leaves no helper", func(t *testing.T) {
		rt := newRuntime(t)
		pidFile := filepath.Join(t.TempDir(), "pid")
		mustRun(t, rt, shReq(t, `sleep 600 >/dev/null 2>&1 & echo $! > `+pidFile))
		waitDead(t, readPID(t, pidFile))
	})
}

func TestRun_IdleAndLoopWatchdog(t *testing.T) {
	tests := []struct {
		name   string
		script string
		idle   time.Duration
		reason string
	}{
		{"idle", `echo started; sleep 30`, 100 * time.Millisecond, sandbox.KillReasonIdleTimeout},
		{"loop", `i=0; while [ $i -lt 40 ]; do echo same line; i=$((i+1)); done; sleep 30`, time.Minute, sandbox.KillReasonLoopDetected},
		{"loop on stderr", `i=0; while [ $i -lt 40 ]; do echo same line >&2; i=$((i+1)); done; sleep 30`, time.Minute, sandbox.KillReasonLoopDetected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newRuntime(t)
			req := shReq(t, tt.script)
			req.IdleTimeout = tt.idle
			start := time.Now()
			res := mustRun(t, rt, req)
			if !res.Killed || res.KillReason != tt.reason {
				t.Errorf("Killed = %v reason = %q, want %q", res.Killed, res.KillReason, tt.reason)
			}
			if res.ExitCode != 137 {
				t.Errorf("exit = %d, want 137", res.ExitCode)
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("took %v", time.Since(start))
			}
		})
	}
	t.Run("an active run is not idle", func(t *testing.T) {
		rt := newRuntime(t)
		req := shReq(t, `for i in 1 2 3 4 5 6; do echo line $i; sleep 0.05; done`)
		req.IdleTimeout = 200 * time.Millisecond
		if res := mustRun(t, rt, req); res.Killed || res.ExitCode != 0 {
			t.Errorf("result = %+v", res)
		}
	})
}

func TestRun_OutputAndLogFile(t *testing.T) {
	rt := newRuntime(t)
	req := shReq(t, `echo to-out; echo to-err >&2`)
	var live bytes.Buffer
	req.Output = &live
	req.LogFilePath = filepath.Join(t.TempDir(), "logs", "run.log")
	res := mustRun(t, rt, req)

	if live.String() != "to-out\n" {
		t.Errorf("Output received %q, want stdout only", live.String())
	}
	if res.Stdout != "to-out" || res.Stderr != "to-err" {
		t.Errorf("result = %+v", res)
	}
	log, err := os.ReadFile(req.LogFilePath)
	if err != nil || !strings.Contains(string(log), "to-out") || !strings.Contains(string(log), "to-err") {
		t.Errorf("log file = %q, %v", log, err)
	}
	// A second run appends, like the Docker runtime.
	mustRun(t, rt, req)
	if log, _ := os.ReadFile(req.LogFilePath); strings.Count(string(log), "to-out") != 2 {
		t.Errorf("log file was not appended: %q", log)
	}
}

func TestLayout(t *testing.T) {
	rt := newRuntime(t)
	req := okReq(t)
	got, err := sandbox.LayoutOf(rt, req)
	if err != nil || got.Home != filepath.Join(rt.cfg.RunRoot, "t1", "home") {
		t.Errorf("LayoutOf = %+v, %v", got, err)
	}
	req.TaskID = "../escape"
	if _, err := sandbox.LayoutOf(rt, req); err == nil {
		t.Error("an invalid task id must be refused")
	}
}

func TestNew(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no HOME")
	}
	for name, root := range map[string]string{"HOME itself": home, "above HOME": filepath.Dir(home), "filesystem root": "/"} {
		if _, err := New(Config{RunRoot: root}); !errors.Is(err, sandbox.ErrUnsafeRoot) {
			t.Errorf("%s: err = %v, want ErrUnsafeRoot", name, err)
		}
	}
	root := filepath.Join(t.TempDir(), "a", "b")
	if _, err := New(Config{RunRoot: root}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("run root: %v, %v", info, err)
	}
}

func TestPruneOrphaned(t *testing.T) {
	rt := newRuntime(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	mk := func(name string, mtime time.Time) {
		p := filepath.Join(rt.cfg.RunRoot, name)
		if err := os.MkdirAll(filepath.Join(p, "home"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk("old-crashed", old)
	mk("fresh", time.Now())
	mk("old-but-running", old)
	rt.active["old-but-running"] = struct{}{}
	// An old symlink planted in the run root is removed, not followed (touch -h dates the link itself).
	link := filepath.Join(rt.cfg.RunRoot, "old-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("touch", "-h", "-t", "202001010000", link).CombinedOutput(); err != nil {
		t.Fatalf("touch -h: %v: %s", err, out)
	}

	n, err := rt.PruneOrphaned(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for name, wantGone := range map[string]bool{"old-crashed": true, "old-link": true, "fresh": false, "old-but-running": false} {
		_, statErr := os.Stat(filepath.Join(rt.cfg.RunRoot, name))
		if gone := os.IsNotExist(statErr); gone != wantGone {
			t.Errorf("%s: gone = %v, want %v", name, gone, wantGone)
		}
	}
	if n != 2 {
		t.Errorf("removed %d, want 2 (the crashed directory and the link)", n)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("pruning followed a symlink out of the run root: %v", err)
	}
}

func TestHostConformance(t *testing.T) {
	conformance.Run(t, newRuntime(t), conformance.Options{
		Prepare: func(r *sandbox.CommandRequest) { r.Profile = "claude_code_notools" },
	})
}
