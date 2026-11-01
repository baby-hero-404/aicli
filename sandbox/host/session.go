//go:build linux || darwin

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/baby-hero-404/aicli/sandbox"
)

var _ sandbox.SessionRuntime = (*Runtime)(nil)

// process implements sandbox.Process for the host runtime: req.Command run as a child with
// pipes on stdin/stdout, in its own process group, under the same private run directory (HOME,
// credential files, session links) Run uses for a one-shot command.
type process struct {
	rt  *Runtime
	req sandbox.CommandRequest
	d   *runDir
	cmd *exec.Cmd

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr bytes.Buffer

	mu         sync.Mutex
	killReason string
	waited     bool
}

// Start implements sandbox.SessionRuntime. Unlike Run, it does not take a context for the
// process's whole lifetime: Start only covers getting the child running, so a cancelled ctx stops
// *launching* it, never a child that is already live and waiting for its first turn. The caller
// controls the child's lifetime through the returned Process (Wait / Kill), exactly as it must
// decide for itself how long an idle, pre-spawned process is worth keeping.
func (r *Runtime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Process, error) {
	d, err := r.prepare(req, false)
	if err != nil {
		return nil, err
	}
	argv0, err := lookPath(req.Command[0], r.cfg.PATH)
	if err != nil {
		d.close()
		return nil, err
	}
	cmd := exec.Command(argv0, req.Command[1:]...)
	cmd.Dir = d.workspace
	cmd.Env = buildEnv(r.cfg, req, d.layout, d.tmp, false)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay

	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.close()
		return nil, fmt.Errorf("host: session stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.close()
		return nil, fmt.Errorf("host: session stdout pipe: %w", err)
	}
	p := &process{rt: r, req: req, d: d, cmd: cmd, stdin: stdin, stdout: stdout}
	cmd.Stderr = &p.stderr

	if ctx.Err() != nil {
		d.close()
		return nil, ctx.Err()
	}
	if err := cmd.Start(); err != nil {
		d.close()
		return nil, fmt.Errorf("host: start session command: %w", err)
	}
	return p, nil
}

func (p *process) Stdin() io.WriteCloser { return p.stdin }
func (p *process) Stdout() io.Reader     { return p.stdout }

// Kill implements sandbox.Process. Safe to call more than once and from a different goroutine
// than Wait: killGroup is idempotent (signalling an already-dead process group is a harmless
// ESRCH), and reason is only latched once so the first real reason wins.
func (p *process) Kill(reason string) error {
	p.mu.Lock()
	if p.killReason == "" {
		p.killReason = reason
	}
	p.mu.Unlock()
	if p.cmd.Process == nil {
		return nil
	}
	killGroup(p.cmd.Process.Pid)
	return nil
}

// Wait implements sandbox.Process. It must be called exactly once; a second call returns an
// error rather than racing os/exec's own double-Wait panic.
func (p *process) Wait() (*sandbox.CommandResult, error) {
	p.mu.Lock()
	if p.waited {
		p.mu.Unlock()
		return nil, errors.New("host: session already waited on")
	}
	p.waited = true
	p.mu.Unlock()

	waitErr := p.cmd.Wait()
	if p.cmd.Process != nil {
		killGroup(p.cmd.Process.Pid) // a helper the command left behind must not outlive it
	}
	defer p.d.close()

	if err := p.d.checkLinks(); err != nil {
		return nil, err
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return nil, fmt.Errorf("host: wait for session command: %w", waitErr)
	}
	p.mu.Lock()
	reason := p.killReason
	p.mu.Unlock()
	return &sandbox.CommandResult{
		ExitCode:               exitCode(p.cmd.ProcessState),
		Stderr:                 strings.TrimSpace(p.stderr.String()),
		UpdatedCredentialFiles: p.d.updatedCredentials(p.req),
		Killed:                 reason != "",
		KillReason:             reason,
	}, nil
}
