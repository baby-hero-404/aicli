// Package sandboxmock provides an in-memory sandbox.Runtime for tests of code that uses aicli,
// so they need no Docker daemon.
package sandboxmock

import (
	"context"
	"io"
	"sync"

	"github.com/baby-hero-404/aicli/sandbox"
)

// Runtime records every request and replies with scripted results. The zero value is ready to
// use and returns an empty successful result.
type Runtime struct {
	mu sync.Mutex
	// Results are returned in order, one per Run call; the last one repeats.
	Results []*sandbox.CommandResult
	// Err, if set, is returned by every Run and RunInteractive call.
	Err error
	// OnRun, if set, is called with each Run request before the result is returned, e.g. to
	// write files into req.Workspace the way a container would.
	OnRun func(req sandbox.CommandRequest)
	// Interactive, if set, replaces the default RunInteractive behavior (returning Err).
	Interactive func(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error

	requests []sandbox.CommandRequest
}

var _ sandbox.Runtime = (*Runtime)(nil)

// Requests returns a copy of the requests received so far.
func (m *Runtime) Requests() []sandbox.CommandRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sandbox.CommandRequest(nil), m.requests...)
}

// Run implements sandbox.Runtime.
func (m *Runtime) Run(_ context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	onRun, err := m.OnRun, m.Err
	var res *sandbox.CommandResult
	if len(m.Results) > 0 {
		res = m.Results[0]
		if len(m.Results) > 1 {
			m.Results = m.Results[1:]
		}
	}
	m.mu.Unlock()

	if onRun != nil {
		onRun(req)
	}
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = &sandbox.CommandResult{}
	}
	return res, nil
}

// RunInteractive implements sandbox.Runtime.
func (m *Runtime) RunInteractive(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	fn, err := m.Interactive, m.Err
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, req, stdin, stdout, stderr)
	}
	return err
}

// Prewarm implements sandbox.Runtime.
func (m *Runtime) Prewarm(context.Context) error { return m.Err }
