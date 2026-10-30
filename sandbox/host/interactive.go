//go:build linux || darwin

package host

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/baby-hero-404/aicli/sandbox"
)

// ptyDrainTimeout bounds how long RunInteractive waits for the last output after the process
// exited.
const ptyDrainTimeout = 2 * time.Second

// RunInteractive runs req attached to a PTY, streaming stdin and output until the process exits
// or ctx ends. req.Command must be exactly one of Config.InteractiveCommands. Like the Docker
// runtime it applies the workspace only (SessionMounts and CredentialFiles are refused), so it is
// meant for login sessions whose files the caller collects from the workspace.
func (r *Runtime) RunInteractive(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	d, err := r.prepare(req, true)
	if err != nil {
		return err
	}
	defer d.close()

	argv0, err := lookPath(req.Command[0], r.cfg.PATH)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv0, req.Command[1:]...)
	cmd.Dir = d.workspace
	cmd.Env = buildEnv(r.cfg, req, d.layout, d.tmp, true)

	// pty.StartWithSize makes the child a session and process-group leader (Setsid + Setctty).
	// Setpgid must not be set as well: setpgid on a session leader fails with EPERM.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return fmt.Errorf("host: start interactive command: %w", err)
	}
	pid := cmd.Process.Pid
	defer ptmx.Close()

	done := make(chan struct{})
	var resizing sync.WaitGroup
	// Runs before ptmx.Close (deferred earlier): pty.Setsize uses the file descriptor directly, so
	// no resize may be in flight when the PTY is closed.
	defer func() {
		close(done)
		resizing.Wait()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	if req.ResizeCh != nil {
		resizing.Add(1)
		go func() {
			defer resizing.Done()
			for {
				select {
				case size, ok := <-req.ResizeCh:
					if !ok {
						return
					}
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(size.Rows), Cols: uint16(size.Cols)})
				case <-done:
					return
				}
			}
		}()
	}
	go func() { _, _ = io.Copy(ptmx, stdin) }()
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(stdout, ptmx) // ends with EIO once the child side is closed
	}()

	_ = cmd.Wait() // the exit status of a login is not an error here, as with the Docker runtime
	killGroup(pid) // helpers the command left behind must not outlive the session
	select {
	case <-outDone:
	case <-time.After(ptyDrainTimeout):
	}
	return ctx.Err()
}
