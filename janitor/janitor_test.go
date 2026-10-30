package janitor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/toolchain"
)

type fakeRecoverer struct {
	calls atomic.Int32
	err   error
}

func (f *fakeRecoverer) RecoverStale(context.Context) (int, error) {
	f.calls.Add(1)
	return 2, f.err
}

type fakePruner struct {
	calls  atomic.Int32
	minAge atomic.Int64
	panics bool
	slow   time.Duration

	mu          sync.Mutex
	running     int
	maxRunning  int
	finishedRun atomic.Int32
}

func (f *fakePruner) PruneOrphaned(ctx context.Context, minAge time.Duration) (int, error) {
	f.calls.Add(1)
	f.minAge.Store(int64(minAge))
	f.mu.Lock()
	f.running++
	f.maxRunning = max(f.maxRunning, f.running)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
		f.finishedRun.Add(1)
	}()
	if f.panics {
		panic("docker went away")
	}
	select {
	case <-time.After(f.slow):
	case <-ctx.Done():
	}
	return 0, nil
}

// logBuf is a goroutine-safe buffer for slog output.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newLogger() (*slog.Logger, *logBuf) {
	buf := &logBuf{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func TestRunOnce_RunsEveryConfiguredTaskAndAppliesDefaults(t *testing.T) {
	log, _ := newLogger()
	rec, pr := &fakeRecoverer{}, &fakePruner{}
	root := t.TempDir()
	old := time.Now().Add(-400 * 24 * time.Hour)
	ws := filepath.Join(root, "workspaces")
	_ = os.MkdirAll(filepath.Join(ws, "old"), 0o755)
	_ = os.Chtimes(filepath.Join(ws, "old"), old, old)

	New(Config{Conversations: rec, Orphans: pr, Filesystem: sandbox.JanitorConfig{WorkspacesRoot: ws}, Logger: log}).RunOnce(context.Background())

	if rec.calls.Load() != 1 || pr.calls.Load() != 1 {
		t.Errorf("recover calls = %d, prune calls = %d, want 1 each", rec.calls.Load(), pr.calls.Load())
	}
	if got := time.Duration(pr.minAge.Load()); got != DefaultOrphanMinAge {
		t.Errorf("orphan min age = %v, want the default %v", got, DefaultOrphanMinAge)
	}
	if _, err := os.Stat(filepath.Join(ws, "old")); !os.IsNotExist(err) {
		t.Error("the idle workspace was not removed with the default TTL")
	}
}

func TestRunOnce_ANeedlessTaskIsNotCreated(t *testing.T) {
	if n := len(New(Config{}).tasks); n != 0 {
		t.Errorf("an empty config made %d tasks", n)
	}
}

// Housekeeping must never take the host down: errors are logged, a panic is contained, and the
// remaining tasks still run.
func TestRunOnce_ErrorsAndPanicsAreContained(t *testing.T) {
	log, buf := newLogger()
	rec := &fakeRecoverer{err: errors.New("database unreachable")}
	pr := &fakePruner{panics: true}

	New(Config{Conversations: rec, Orphans: pr, Logger: log}).RunOnce(context.Background())

	out := buf.String()
	if !strings.Contains(out, "database unreachable") || !strings.Contains(out, "task failed") {
		t.Errorf("the error was not logged as a warning:\n%s", out)
	}
	if !strings.Contains(out, "task panicked") || !strings.Contains(out, "docker went away") {
		t.Errorf("the panic was not contained and logged:\n%s", out)
	}
	if pr.calls.Load() != 1 {
		t.Error("the task after the failing one did not run")
	}
}

func TestStart_RunsAtOnceThenPeriodicallyThenStops(t *testing.T) {
	log, _ := newLogger()
	rec := &fakeRecoverer{}
	j := New(Config{Conversations: rec, ConversationInterval: 20 * time.Millisecond, Logger: log})

	ctx, cancel := context.WithCancel(context.Background())
	j.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for rec.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.calls.Load() < 3 {
		t.Fatalf("only %d runs: the startup pass and two periodic ones were expected", rec.calls.Load())
	}

	cancel()
	done := make(chan struct{})
	go func() { j.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the workers did not stop when the context ended")
	}
	n := rec.calls.Load()
	time.Sleep(60 * time.Millisecond)
	if rec.calls.Load() != n {
		t.Error("a task kept running after the context ended")
	}
}

// A task that takes longer than its interval is not started again on top of itself.
func TestStart_ATaskNeverOverlapsItself(t *testing.T) {
	log, buf := newLogger()
	pr := &fakePruner{slow: 120 * time.Millisecond}
	j := New(Config{Orphans: pr, OrphanInterval: 10 * time.Millisecond, Logger: log})

	ctx, cancel := context.WithCancel(context.Background())
	j.Start(ctx)
	time.Sleep(400 * time.Millisecond)
	cancel()
	j.Wait()

	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.maxRunning != 1 {
		t.Errorf("up to %d runs of the same task at once", pr.maxRunning)
	}
	if !strings.Contains(buf.String(), "skipping") {
		t.Error("the skipped runs were not logged")
	}
}

func TestRunTask_HasATimeoutAndSaysSo(t *testing.T) {
	log, buf := newLogger()
	pr := &fakePruner{slow: time.Minute}
	j := New(Config{Orphans: pr, TaskTimeout: 30 * time.Millisecond, Logger: log})
	start := time.Now()
	j.RunOnce(context.Background())
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a stuck task held the pass for %v despite its timeout", d)
	}
	if out := buf.String(); !strings.Contains(out, "timed out") || !strings.Contains(out, "prune orphaned containers") {
		t.Errorf("a task that hit its timeout left no warning:\n%s", out)
	}
}

// Stopping the host is not a failure: a task cut short by it logs nothing.
func TestRunTask_ShutdownIsNotReportedAsAFailure(t *testing.T) {
	log, buf := newLogger()
	pr := &fakePruner{slow: time.Minute}
	j := New(Config{Orphans: pr, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	j.RunOnce(ctx)
	if out := buf.String(); strings.Contains(out, "timed out") || strings.Contains(out, "task failed") {
		t.Errorf("shutdown was reported as a problem:\n%s", out)
	}
}

// The toolchain task has its own, longer limit: installing three tools can take longer than the
// default for the cheap tasks.
func TestToolchainTaskHasItsOwnTimeout(t *testing.T) {
	in := &toolchain.Installer{Store: mustStore(t)}
	j := New(Config{Toolchain: in, TaskTimeout: time.Second})
	var got, other time.Duration
	for _, tk := range j.tasks {
		if tk.name == "sync the CLI toolchain" {
			got = tk.timeout
		}
	}
	if got != DefaultToolchainTimeout || got <= DefaultTaskTimeout {
		t.Errorf("toolchain timeout = %v, want %v (longer than the %v default)", got, DefaultToolchainTimeout, DefaultTaskTimeout)
	}
	j = New(Config{Toolchain: in, ToolchainTimeout: 2 * time.Hour, Orphans: &fakePruner{}, TaskTimeout: time.Second})
	for _, tk := range j.tasks {
		if tk.name == "sync the CLI toolchain" {
			got = tk.timeout
		} else {
			other = tk.timeout
		}
	}
	if got != 2*time.Hour || other != time.Second {
		t.Errorf("toolchain = %v, other = %v, want 2h and 1s", got, other)
	}
}

func mustStore(t *testing.T) *toolchain.Store {
	t.Helper()
	s, err := toolchain.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A misconfigured cleanup root is refused and reported, and nothing is deleted.
func TestRunOnce_UnsafeFilesystemRootIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := time.Now().Add(-400 * 24 * time.Hour)
	notes := filepath.Join(home, "notes.txt")
	_ = os.WriteFile(notes, []byte("mine"), 0o644)
	_ = os.Chtimes(notes, old, old)

	log, buf := newLogger()
	New(Config{Filesystem: sandbox.JanitorConfig{WorkspacesRoot: home}, Logger: log}).RunOnce(context.Background())

	if _, err := os.Stat(notes); err != nil {
		t.Error("a file in HOME was deleted")
	}
	if !strings.Contains(buf.String(), "unsafe cleanup root") {
		t.Errorf("the refusal was not logged:\n%s", buf.String())
	}
}
