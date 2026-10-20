package sandboxmock

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

func TestRuntime_ScriptedResultsAndRecording(t *testing.T) {
	m := &Runtime{Results: []*sandbox.CommandResult{{ExitCode: 1}, {ExitCode: 2}}}
	ctx := context.Background()
	want := []int{1, 2, 2} // the last scripted result repeats
	for i, code := range want {
		res, err := m.Run(ctx, sandbox.CommandRequest{TaskID: string(rune('a' + i))})
		if err != nil || res.ExitCode != code {
			t.Fatalf("call %d: %+v, %v; want exit %d", i, res, err, code)
		}
	}
	if reqs := m.Requests(); len(reqs) != 3 || reqs[2].TaskID != "c" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestRuntime_DefaultsAndErrors(t *testing.T) {
	ctx := context.Background()
	var zero Runtime
	if res, err := zero.Run(ctx, sandbox.CommandRequest{}); err != nil || res == nil || res.ExitCode != 0 {
		t.Errorf("zero value should succeed, got %+v, %v", res, err)
	}
	boom := errors.New("boom")
	failing := &Runtime{Err: boom}
	if _, err := failing.Run(ctx, sandbox.CommandRequest{}); !errors.Is(err, boom) {
		t.Errorf("Run err = %v", err)
	}
	if err := failing.RunInteractive(ctx, sandbox.CommandRequest{}, strings.NewReader(""), io.Discard, io.Discard); !errors.Is(err, boom) {
		t.Errorf("RunInteractive err = %v", err)
	}
	if err := failing.Prewarm(ctx); !errors.Is(err, boom) {
		t.Errorf("Prewarm err = %v", err)
	}
}

func TestRuntime_OnRunAndInteractive(t *testing.T) {
	called := false
	m := &Runtime{
		OnRun: func(req sandbox.CommandRequest) { called = req.Workspace == "/ws" },
		Interactive: func(_ context.Context, _ sandbox.CommandRequest, stdin io.Reader, stdout, _ io.Writer) error {
			b, _ := io.ReadAll(stdin)
			_, _ = stdout.Write(b)
			return nil
		},
	}
	if _, err := m.Run(context.Background(), sandbox.CommandRequest{Workspace: "/ws"}); err != nil || !called {
		t.Errorf("OnRun not called: %v", err)
	}
	var out strings.Builder
	if err := m.RunInteractive(context.Background(), sandbox.CommandRequest{}, strings.NewReader("hi"), &out, io.Discard); err != nil || out.String() != "hi" {
		t.Errorf("interactive: %q, %v", out.String(), err)
	}
}
