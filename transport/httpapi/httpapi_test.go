package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/conversation/memstore"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/sandboxmock"
	"github.com/baby-hero-404/aicli/transport/httpapi"
)

const sessionID = "11111111-1111-4111-8111-111111111111" // the CLI session ID in the basic fixture

type resolver struct{ workspace string }

func (r resolver) Resolve(context.Context, conversation.Principal, conversation.Conversation) (run.Request, error) {
	return run.Request{Workspace: r.workspace}, nil
}

func (resolver) SaveCredentials(context.Context, conversation.Principal, conversation.Conversation, map[string]string) error {
	return nil
}

type countingAuthorizer struct{ calls atomic.Int64 }

func (c *countingAuthorizer) Authorize(ctx context.Context, p conversation.Principal, a conversation.Action, cv *conversation.Conversation) error {
	c.calls.Add(1)
	return conversation.OwnerOnly{}.Authorize(ctx, p, a, cv)
}

func fixture(t *testing.T, name string) *sandbox.CommandResult {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "run", "stream", "agy", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return &sandbox.CommandResult{Stdout: string(b)}
}

type env struct {
	t         *testing.T
	srv       *httptest.Server
	svc       *conversation.Service
	authz     *countingAuthorizer
	workspace string
	rt        *sandboxmock.Runtime
}

type tweak func(*httpapi.Config)

func newEnv(t *testing.T, results []*sandbox.CommandResult, tweaks ...tweak) *env {
	t.Helper()
	e := &env{t: t, authz: &countingAuthorizer{}, workspace: t.TempDir(), rt: &sandboxmock.Runtime{Results: results}}
	svc, err := conversation.NewService(conversation.Config{
		Runner: &run.Runner{Runtime: e.rt}, Store: memstore.New(), Authorizer: e.authz,
		Resolver: resolver{e.workspace}, Profiles: []string{"antigravity"},
		EventPoll: 20 * time.Millisecond, CancelPoll: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	cfg := httpapi.Config{
		Service: svc,
		Authenticate: func(r *http.Request) (conversation.Principal, error) {
			if u := r.Header.Get("X-User"); u != "" {
				return conversation.Principal(u), nil
			}
			return "", errors.New("no user")
		},
		AllowNoOrigin: true, // httptest clients send no Origin; origin tests turn it off
		Heartbeat:     20 * time.Millisecond,
	}
	for _, tw := range tweaks {
		tw(&cfg)
	}
	h, err := httpapi.NewHandler(cfg)
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

type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, r.body)
	}
}

func (r reply) errCode(t *testing.T) string {
	t.Helper()
	var b struct {
		Error struct{ Code string }
	}
	r.json(t, &b)
	return b.Error.Code
}

type req struct {
	method, path, user, body string
	header                   map[string]string
}

func (e *env) do(r req) reply {
	e.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr, err := http.NewRequest(r.method, e.srv.URL+r.path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if r.user != "" {
		hr.Header.Set("X-User", r.user)
	}
	if r.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func (e *env) create(user string) string {
	e.t.Helper()
	r := e.do(req{method: "POST", path: "/conversations", user: user, body: `{"profile":"antigravity","title":"t"}`})
	if r.status != http.StatusCreated {
		e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	var c struct{ ID string }
	r.json(e.t, &c)
	return c.ID
}

func (e *env) send(user, id, msg, cid string) reply {
	body, _ := json.Marshal(map[string]string{"message": msg, "clientMessageId": cid})
	return e.do(req{method: "POST", path: "/conversations/" + id + "/messages", user: user, body: string(body)})
}

// settle waits until the conversation has no running turn.
func (e *env) settle(user, id string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := e.do(req{method: "GET", path: "/conversations/" + id, user: user})
		var c struct{ ActiveTurnID string }
		r.json(e.t, &c)
		if c.ActiveTurnID == "" {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("turn still running after 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- server-sent events client ----------------------------------------------------------

type sseEvent struct {
	ID, Event, Data string
	Comment         bool
}

type sseConn struct {
	t      *testing.T
	resp   *http.Response
	sc     *bufio.Scanner
	cancel context.CancelFunc
}

func (e *env) open(user, id, lastEventID string) *sseConn {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	hr, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/conversations/"+id+"/stream", nil)
	hr.Header.Set("X-User", user)
	if lastEventID != "" {
		hr.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		e.t.Fatalf("stream: %d %s", resp.StatusCode, b)
	}
	c := &sseConn{t: e.t, resp: resp, sc: bufio.NewScanner(resp.Body), cancel: cancel}
	e.t.Cleanup(c.close)
	return c
}

func (c *sseConn) close() { c.cancel(); c.resp.Body.Close() }

// next returns the next message: an event, or a comment line.
func (c *sseConn) next() sseEvent {
	c.t.Helper()
	var ev sseEvent
	for c.sc.Scan() {
		line := c.sc.Text()
		switch {
		case line == "":
			if ev.Event != "" || ev.Comment {
				return ev
			}
		case strings.HasPrefix(line, ":"):
			ev.Comment = true
		case strings.HasPrefix(line, "id: "):
			ev.ID = line[4:]
		case strings.HasPrefix(line, "event: "):
			ev.Event = line[7:]
		case strings.HasPrefix(line, "data: "):
			ev.Data = line[6:]
		}
	}
	c.t.Fatalf("stream ended: %v", c.sc.Err())
	return ev
}

// events reads real events (skipping comments) until one of kind stop arrives.
func (c *sseConn) until(stop string) []sseEvent {
	c.t.Helper()
	var out []sseEvent
	for {
		ev := c.next()
		if ev.Comment && ev.Event == "" {
			continue
		}
		out = append(out, ev)
		if ev.Event == stop {
			return out
		}
	}
}
