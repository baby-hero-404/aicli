//go:build linux || darwin

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/internal/watch"
)

const (
	// waitDelay bounds how long Run waits for the output pipes after the process has exited,
	// in case a helper that outlived it still holds them.
	waitDelay = 5 * time.Second
	// defaultStallInterval is how often the idle and loop watchdog looks at the output.
	defaultStallInterval = 5 * time.Second
)

// Runtime runs sandbox requests as child processes. It implements sandbox.Runtime.
type Runtime struct {
	cfg        Config
	log        *slog.Logger
	stallEvery time.Duration // watchdog poll interval; shortened by tests

	mu     sync.Mutex
	active map[string]struct{} // task IDs with a run directory in use
}

var (
	_ sandbox.Runtime        = (*Runtime)(nil)
	_ sandbox.LayoutProvider = (*Runtime)(nil)
)

// New returns a host runtime. It creates RunRoot (mode 0700) and refuses one that covers the host
// user's HOME.
func New(cfg Config) (*Runtime, error) {
	if cfg.RunRoot == "" {
		cfg.RunRoot = filepath.Join(os.TempDir(), "aicli-host")
	}
	abs, err := filepath.Abs(cfg.RunRoot)
	if err != nil {
		return nil, fmt.Errorf("host: run root %q: %w", cfg.RunRoot, err)
	}
	cfg.RunRoot = abs
	if cfg.PATH == "" {
		cfg.PATH = defaultPATH
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if coversHome(cfg.RunRoot) {
		return nil, fmt.Errorf("%w: run root %q covers the host user's HOME", sandbox.ErrUnsafeRoot, cfg.RunRoot)
	}
	if err := os.MkdirAll(cfg.RunRoot, 0o700); err != nil {
		return nil, fmt.Errorf("host: create run root: %w", err)
	}
	// Symlinks in the run root path are resolved once, so every later path is the real one.
	if cfg.RunRoot, err = filepath.EvalSymlinks(cfg.RunRoot); err != nil {
		return nil, fmt.Errorf("host: resolve run root: %w", err)
	}
	if cfg.AllowTools {
		cfg.Logger.Warn("host runtime: tools are enabled; CLIs run unconfined with this process's rights")
	}
	return &Runtime{cfg: cfg, log: cfg.Logger, stallEvery: defaultStallInterval, active: make(map[string]struct{})}, nil
}

// Layout implements sandbox.LayoutProvider: the workspace is the host directory itself and HOME
// is a private directory under RunRoot.
func (r *Runtime) Layout(req sandbox.CommandRequest) (sandbox.Layout, error) {
	return layoutFor(r.cfg, req)
}

// Prewarm implements sandbox.Runtime and does nothing: there is no image to pull.
func (r *Runtime) Prewarm(context.Context) error { return nil }

// link is a session mount: a symlink at target pointing at the session directory source.
type link struct{ target, source string }

// runDir is the private directory of one run and what was placed in it.
type runDir struct {
	rt        *Runtime
	taskID    string
	root, tmp string
	workspace string // real location
	layout    sandbox.Layout
	links     []link
	creds     map[string]string // container-style target -> file path (the same string here)
}

// prepare validates req and creates its run directory with the session links and credential files
// in place. Nothing is created when the request is refused. The caller must call close.
func (r *Runtime) prepare(req sandbox.CommandRequest, interactive bool) (*runDir, error) {
	layout, err := checkRequest(r.cfg, req, interactive)
	if err != nil {
		r.log.Info("host runtime refused a request", "task_id", req.TaskID, "profile", req.Profile, "reason", err.Error())
		return nil, err
	}

	// Resolve every directory the child will be given, before creating anything.
	workspace, err := resolveDir(layout.Workspace, r.cfg.AllowedRoots)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]string, len(req.SessionMounts))
	for target, src := range req.SessionMounts {
		if sources[target], err = resolveDir(src, r.cfg.AllowedRoots); err != nil {
			return nil, err
		}
	}

	d := &runDir{rt: r, taskID: req.TaskID, root: filepath.Join(r.cfg.RunRoot, req.TaskID), layout: layout, creds: map[string]string{}}
	d.tmp = filepath.Join(d.root, "tmp")
	r.mu.Lock()
	if _, busy := r.active[req.TaskID]; busy {
		r.mu.Unlock()
		return nil, fmt.Errorf("host: task %q already has a run in progress", req.TaskID)
	}
	if err := os.Mkdir(d.root, 0o700); err != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("host: create run directory: %w", err)
	}
	r.active[req.TaskID] = struct{}{}
	r.mu.Unlock()

	if err := d.populate(req, workspace, sources); err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

func (d *runDir) populate(req sandbox.CommandRequest, workspace string, sources map[string]string) error {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("host: create workspace: %w", err)
	}
	var err error
	// The workspace now exists, so its final location can be read; it must be a directory.
	if d.workspace, err = filepath.EvalSymlinks(workspace); err != nil {
		return fmt.Errorf("host: resolve workspace: %w", err)
	}
	if info, err := os.Stat(d.workspace); err != nil || !info.IsDir() {
		return fmt.Errorf("host: workspace %q is not a directory", d.workspace)
	}
	for _, dir := range []string{d.layout.Home, d.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("host: create run directory: %w", err)
		}
	}
	for target, src := range sources {
		if err := os.MkdirAll(src, 0o700); err != nil {
			return fmt.Errorf("host: create session directory: %w", err)
		}
		// Link to the final location, so a source swapped for a symlink since the check leads nowhere new.
		if src, err = filepath.EvalSymlinks(src); err != nil {
			return fmt.Errorf("host: resolve session directory: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("host: create session link dir: %w", err)
		}
		if err := os.Symlink(src, target); err != nil {
			return fmt.Errorf("host: link session directory: %w", err)
		}
		d.links = append(d.links, link{target: target, source: src})
	}
	for target, content := range req.CredentialFiles {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("host: create credential dir: %w", err)
		}
		if err := writeFileReplacing(target, []byte(content)); err != nil {
			return fmt.Errorf("host: write credential file: %w", err)
		}
		d.creds[target] = target
	}
	return nil
}

// close removes the run directory (symlinks are removed, never followed) and frees the task ID.
func (d *runDir) close() {
	if err := os.RemoveAll(d.root); err != nil {
		d.rt.log.Warn("host runtime: remove run directory", "task_id", d.taskID, "error", err)
	}
	d.rt.mu.Lock()
	delete(d.rt.active, d.taskID)
	d.rt.mu.Unlock()
}

// checkLinks reports ErrSessionDetached when the child replaced a session link.
func (d *runDir) checkLinks() error {
	for _, l := range d.links {
		info, err := os.Lstat(l.target)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrSessionDetached, l.target)
		}
		if dest, err := os.Readlink(l.target); err != nil || dest != l.source {
			return fmt.Errorf("%w: %s", ErrSessionDetached, l.target)
		}
	}
	return nil
}

// updatedCredentials returns the credential files whose content differs from what was written.
func (d *runDir) updatedCredentials(req sandbox.CommandRequest) map[string]string {
	var out map[string]string
	for target, file := range d.creds {
		data, err := readRegularFile(file)
		if err != nil || string(data) == req.CredentialFiles[target] {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[target] = string(data)
	}
	return out
}

// killGroup sends SIGKILL to the process group led by pid.
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// Run executes req as a child process in its own process group and removes its run directory
// afterwards. Idle and loop watchdogs kill a stalled process; credential files it rewrote are
// returned in CommandResult.UpdatedCredentialFiles. A cancelled context or a timeout kills the
// process group and returns the context's error, as the Docker runtime does.
func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	d, err := r.prepare(req, false)
	if err != nil {
		return nil, err
	}
	defer d.close()

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	argv0, err := lookPath(req.Command[0], r.cfg.PATH)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv0, req.Command[1:]...)
	cmd.Dir = d.workspace
	cmd.Env = buildEnv(r.cfg, req, d.layout, d.tmp, false)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay

	var stdout, stderr bytes.Buffer
	var stdoutW, stderrW io.Writer = &stdout, &stderr
	if req.LogFilePath != "" {
		if err := os.MkdirAll(filepath.Dir(req.LogFilePath), 0o755); err != nil {
			return nil, fmt.Errorf("host: create run log dir: %w", err)
		}
		logFile, err := os.OpenFile(req.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("host: open run log file: %w", err)
		}
		defer logFile.Close()
		stdoutW, stderrW = io.MultiWriter(&stdout, logFile), io.MultiWriter(&stderr, logFile)
	}
	// One activity/loop pair for both streams: a repeated error can land on either.
	activity, loopDet := watch.NewActivity(io.Discard), watch.NewLoopDetector(io.Discard)
	stdoutW, stderrW = io.MultiWriter(stdoutW, activity, loopDet), io.MultiWriter(stderrW, activity, loopDet)
	if req.Output != nil {
		// Last, so the buffers, log file and watchdog have seen a chunk before the caller does.
		stdoutW = io.MultiWriter(stdoutW, req.Output)
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("host: start command: %w", err)
	}
	pid := cmd.Process.Pid

	idle := req.IdleTimeout
	if idle <= 0 {
		idle = sandbox.DefaultIdleTimeout
	}
	var stallMu sync.Mutex
	var stallReason string
	watchDone := make(chan struct{})
	go watch.Stall(watchDone, activity, loopDet, idle, r.stallEvery, func(reason string) {
		stallMu.Lock()
		stallReason = reason
		stallMu.Unlock()
		killGroup(pid)
	})
	// The context kills the group too; stopped as soon as the process has been waited for.
	go func() {
		select {
		case <-ctx.Done():
			killGroup(pid)
		case <-watchDone:
		}
	}()

	waitErr := cmd.Wait()
	close(watchDone)
	killGroup(pid) // helpers the command left behind must not outlive the run

	stallMu.Lock()
	reason := stallReason
	stallMu.Unlock()
	if ctxErr := ctx.Err(); ctxErr != nil && reason == "" {
		return nil, ctxErr
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return nil, fmt.Errorf("host: wait for command: %w", waitErr)
	}
	if err := d.checkLinks(); err != nil {
		return nil, err
	}
	return &sandbox.CommandResult{
		ExitCode:               exitCode(cmd.ProcessState),
		Stdout:                 strings.TrimSpace(stdout.String()),
		Stderr:                 strings.TrimSpace(stderr.String()),
		UpdatedCredentialFiles: d.updatedCredentials(req),
		Killed:                 reason != "",
		KillReason:             reason,
	}, nil
}

// exitCode returns the exit status, or 128+signal for a process the kernel or a watchdog killed,
// the way a shell and the Docker runtime report it.
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}
