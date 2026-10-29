package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/conversation/memstore"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/transport/httpapi"
)

type sandboxResult = sandbox.CommandResult

// blockingRuntime starts a run, writes an init line, then waits until cancelled.
type blockingRuntime struct {
	sandbox.StubRuntime
	started chan struct{}
}

func newBlockingRuntime() *blockingRuntime { return &blockingRuntime{started: make(chan struct{}, 4)} }

func (b *blockingRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	if req.Output != nil {
		_, _ = io.WriteString(req.Output, `{"event":"init","conversation_id":"blocking","init":{}}`+"\n")
	}
	b.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

func newEnvWithRuntime(t *testing.T, rt sandbox.Runtime) *env {
	t.Helper()
	e := &env{t: t, authz: &countingAuthorizer{}, workspace: t.TempDir()}
	svc, err := conversation.NewService(conversation.Config{
		Runner: &run.Runner{Runtime: rt}, Store: memstore.New(), Authorizer: e.authz,
		Resolver: resolver{e.workspace}, Profiles: []string{"antigravity"},
		EventPoll: 20 * time.Millisecond, CancelPoll: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	h, err := httpapi.NewHandler(httpapi.Config{
		Service: svc, AllowNoOrigin: true,
		Authenticate: func(r *http.Request) (conversation.Principal, error) {
			return conversation.Principal(r.Header.Get("X-User")), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(h)
	t.Cleanup(func() {
		e.srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return e
}
