//go:build !linux && !darwin

package host

import (
	"context"
	"io"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// Runtime is a placeholder on systems the host runtime does not support; New never returns one.
type Runtime struct{}

var _ sandbox.Runtime = (*Runtime)(nil)

// New returns ErrUnsupportedOS.
func New(Config) (*Runtime, error) { return nil, ErrUnsupportedOS }

// Layout implements sandbox.LayoutProvider and returns ErrUnsupportedOS.
func (*Runtime) Layout(sandbox.CommandRequest) (sandbox.Layout, error) {
	return sandbox.Layout{}, ErrUnsupportedOS
}

// Prewarm implements sandbox.Runtime and returns ErrUnsupportedOS.
func (*Runtime) Prewarm(context.Context) error { return ErrUnsupportedOS }

// Run implements sandbox.Runtime and returns ErrUnsupportedOS.
func (*Runtime) Run(context.Context, sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	return nil, ErrUnsupportedOS
}

// RunInteractive implements sandbox.Runtime and returns ErrUnsupportedOS.
func (*Runtime) RunInteractive(context.Context, sandbox.CommandRequest, io.Reader, io.Writer, io.Writer) error {
	return ErrUnsupportedOS
}

// PruneOrphaned returns ErrUnsupportedOS.
func (*Runtime) PruneOrphaned(context.Context, time.Duration) (int, error) {
	return 0, ErrUnsupportedOS
}
