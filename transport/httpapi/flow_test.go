package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/transport/httpapi"
)

func TestFullFlow(t *testing.T) {
	e := newEnv(t, []*sandboxResult{fixture(t, "agy-1.4.8-tools.jsonl")})
	id := e.create("alice")

	stream := e.open("alice", id, "")
	r := e.send("alice", id, "read the note", "m1")
	if r.status != http.StatusAccepted {
		t.Fatalf("send: %d %s", r.status, r.body)
	}
	var turn struct {
		ID     string
		Status string
	}
	r.json(t, &turn)
	if turn.ID == "" || turn.Status != "running" {
		t.Errorf("turn = %+v", turn)
	}

	evs := stream.until("turn.completed")
	var kinds []string
	for i, ev := range evs {
		kinds = append(kinds, ev.Event)
		if ev.ID != strconv.Itoa(i+1) {
			t.Errorf("event %d has id %q, want %d", i, ev.ID, i+1)
		}
		if strings.Contains(ev.Data, "\n") {
			t.Errorf("event data contains a raw newline: %q", ev.Data)
		}
	}
	want := "user.message turn.started tool.use tool.result assistant.text turn.completed"
	if strings.Join(kinds, " ") != want {
		t.Errorf("kinds = %v", kinds)
	}
	e.settle("alice", id)

	// History is the same list.
	r = e.do(req{method: "GET", path: "/conversations/" + id + "/events?after=2&limit=3", user: "alice"})
	var page struct{ Items []struct{ Seq int } }
	r.json(t, &page)
	if r.status != 200 || len(page.Items) != 3 || page.Items[0].Seq != 3 {
		t.Errorf("events page: %d %s", r.status, r.body)
	}

	// List and get.
	r = e.do(req{method: "GET", path: "/conversations?limit=5", user: "alice"})
	var list struct{ Items []struct{ ID string } }
	r.json(t, &list)
	if r.status != 200 || len(list.Items) != 1 || list.Items[0].ID != id {
		t.Errorf("list: %d %s", r.status, r.body)
	}

	// Export, both formats.
	r = e.do(req{method: "GET", path: "/conversations/" + id + "/export?format=md", user: "alice"})
	if r.status != 200 || !strings.Contains(r.body, "secret-marker") || !strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("export md: %d %v\n%s", r.status, r.header, r.body)
	}
	r = e.do(req{method: "GET", path: "/conversations/" + id + "/export?format=json&turnId=" + turn.ID, user: "alice"})
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Errorf("export json: %d %v", r.status, r.header)
	}
}

func TestStreamResumesFromLastEventIDWithoutGapOrDuplicate(t *testing.T) {
	e := newEnv(t, []*sandboxResult{fixture(t, "agy-1.4.8-tools.jsonl")})
	id := e.create("alice")
	e.send("alice", id, "go", "m1")
	e.settle("alice", id)

	// The whole history is 6 events; a client that saw the first 4 reconnects.
	c := e.open("alice", id, "4")
	evs := c.until("turn.completed")
	if len(evs) != 2 || evs[0].ID != "5" || evs[1].ID != "6" {
		t.Errorf("after reconnect: %+v", evs)
	}
}

func TestOneStreamSpansConsecutiveTurnsAndHeartbeatsWhileIdle(t *testing.T) {
	e := newEnv(t, []*sandboxResult{fixture(t, "agy-1.4.8-basic.jsonl"), fixture(t, "agy-1.4.8-resume.jsonl")})
	id := e.create("alice")
	c := e.open("alice", id, "")

	e.send("alice", id, "one", "m1")
	first := c.until("turn.completed")
	e.settle("alice", id)

	// Idle: the stream must stay open and keep sending heartbeats.
	if hb := c.next(); !hb.Comment {
		t.Errorf("expected a heartbeat while idle, got %+v", hb)
	}

	e.send("alice", id, "two", "m2")
	second := c.until("turn.completed")
	if first[len(first)-1].ID == second[len(second)-1].ID || len(second) < 3 {
		t.Errorf("second turn on the same stream: %+v", second)
	}
	if second[0].Event != "user.message" {
		t.Errorf("second turn starts with %q", second[0].Event)
	}
}

func TestStreamCapPerPrincipal(t *testing.T) {
	e := newEnv(t, nil, func(c *httpapi.Config) { c.MaxStreamsPerPrincipal = 2 })
	id := e.create("alice")
	e.open("alice", id, "")
	e.open("alice", id, "")
	r := e.do(req{method: "GET", path: "/conversations/" + id + "/stream", user: "alice"})
	if r.status != http.StatusTooManyRequests || r.errCode(t) != "rate_limited" {
		t.Errorf("third stream: %d %s", r.status, r.body)
	}
	other := e.create("bob")
	e.open("bob", other, "") // another principal is unaffected
}

func TestCancelOverHTTP(t *testing.T) {
	rt := newBlockingRuntime()
	e := newEnvWithRuntime(t, rt)
	id := e.create("alice")
	var turn struct{ ID string }
	e.send("alice", id, "go", "m1").json(t, &turn)
	<-rt.started

	r := e.do(req{method: "POST", path: "/conversations/" + id + "/turns/" + turn.ID + "/cancel", user: "alice"})
	if r.status != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	e.settle("alice", id)
	r = e.do(req{method: "POST", path: "/conversations/" + id + "/turns/" + turn.ID + "/cancel", user: "alice"})
	if r.status != http.StatusConflict || r.errCode(t) != "turn_not_running" {
		t.Errorf("cancel after finish: %d %s", r.status, r.body)
	}
}

func TestSecondSendWhileRunningIs409(t *testing.T) {
	rt := newBlockingRuntime()
	e := newEnvWithRuntime(t, rt)
	id := e.create("alice")
	e.send("alice", id, "go", "m1")
	<-rt.started
	r := e.send("alice", id, "again", "m2")
	if r.status != http.StatusConflict || r.errCode(t) != "turn_in_progress" {
		t.Errorf("%d %s", r.status, r.body)
	}
}

func TestSendRetryIsIdempotent(t *testing.T) {
	e := newEnv(t, []*sandboxResult{fixture(t, "agy-1.4.8-basic.jsonl")})
	id := e.create("alice")
	a := e.send("alice", id, "go", "same")
	b := e.send("alice", id, "go", "same")
	if a.status != http.StatusAccepted || b.status != http.StatusOK {
		t.Errorf("statuses: %d, %d", a.status, b.status)
	}
	var ta, tb struct{ ID string }
	a.json(t, &ta)
	b.json(t, &tb)
	if ta.ID != tb.ID {
		t.Errorf("retry returned another turn: %q vs %q", ta.ID, tb.ID)
	}
	e.settle("alice", id)
	if n := len(e.rt.Requests()); n != 1 {
		t.Errorf("CLI runs = %d", n)
	}
}

func TestMountsUnderAPrefix(t *testing.T) {
	e := newEnv(t, nil)
	h, err := httpapi.NewHandler(httpapi.Config{
		Service:      e.svc,
		Authenticate: func(*http.Request) (conversation.Principal, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/aicli/", http.StripPrefix("/api/aicli", h))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/aicli/conversations", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestNewHandlerRequiresItsDependencies(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := httpapi.NewHandler(httpapi.Config{Authenticate: func(*http.Request) (conversation.Principal, error) { return "", nil }}); err == nil {
		t.Error("missing Service accepted")
	}
	if _, err := httpapi.NewHandler(httpapi.Config{Service: e.svc}); err == nil {
		t.Error("missing Authenticate accepted")
	}
}
