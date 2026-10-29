package httpapi_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/transport/httpapi"
)

// Nothing a client sends may change how a CLI is executed. Every field below is something an
// attacker would try; each must be rejected before anything is stored or run.
func TestClientCannotControlExecution(t *testing.T) {
	forbidden := []string{"command", "args", "extraArgs", "image", "workspace", "env", "secretEnv", "mounts",
		"sessionMounts", "sessionDir", "credentialFiles", "resumeSessionId", "networkMode", "setup", "cleanup", "timeout"}
	e := newEnv(t, nil)
	id := e.create("alice")

	for _, field := range forbidden {
		t.Run(field, func(t *testing.T) {
			for _, c := range []struct{ path, body string }{
				{"/conversations", fmt.Sprintf(`{"profile":"antigravity","%s":"x"}`, field)},
				{"/conversations/" + id + "/messages", fmt.Sprintf(`{"message":"hi","clientMessageId":"m1","%s":"x"}`, field)},
			} {
				r := e.do(req{method: "POST", path: c.path, user: "alice", body: c.body})
				if r.status != http.StatusBadRequest || r.errCode(t) != "unknown_field" {
					t.Errorf("POST %s with %q: %d %s", c.path, field, r.status, r.body)
				}
			}
		})
	}
	if n := len(e.rt.Requests()); n != 0 {
		t.Errorf("%d CLI runs were started by rejected requests", n)
	}
	r := e.do(req{method: "GET", path: "/conversations/" + id + "/events", user: "alice"})
	if strings.Contains(r.body, `"user.message"`) {
		t.Errorf("a rejected send was recorded: %s", r.body)
	}
}

func TestOtherPrincipalsSeeNotFoundOnEveryEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	id := e.create("alice")
	base := "/conversations/" + id
	for _, c := range []struct{ method, path, body string }{
		{"GET", base, ""},
		{"GET", base + "/events", ""},
		{"GET", base + "/stream", ""},
		{"GET", base + "/export", ""},
		{"POST", base + "/messages", `{"message":"hi","clientMessageId":"m1"}`},
		{"POST", base + "/turns/abc/cancel", ""},
		{"GET", "/conversations/does-not-exist", ""},
	} {
		r := e.do(req{method: c.method, path: c.path, user: "mallory", body: c.body})
		if r.status != http.StatusNotFound || r.errCode(t) != "not_found" {
			t.Errorf("%s %s as another principal: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// And they cannot find it by listing.
	var list struct{ Items []any }
	e.do(req{method: "GET", path: "/conversations", user: "mallory"}).json(t, &list)
	if len(list.Items) != 0 {
		t.Errorf("mallory listed %d conversations", len(list.Items))
	}
}

func TestUnauthenticatedRequestsTouchNothing(t *testing.T) {
	e := newEnv(t, nil)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/conversations", `{"profile":"antigravity"}`},
		{"GET", "/conversations", ""},
		{"GET", "/conversations/x", ""},
		{"GET", "/conversations/x/stream", ""},
		{"GET", "/conversations/x/export", ""},
	} {
		r := e.do(req{method: c.method, path: c.path, body: c.body}) // no user
		if r.status != http.StatusUnauthorized || r.errCode(t) != "unauthenticated" {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	if n := e.authz.calls.Load(); n != 0 {
		t.Errorf("the authorizer was called %d times for unauthenticated requests", n)
	}
}

func TestOriginPolicy(t *testing.T) {
	allowed := "https://app.example.com"
	e := newEnv(t, nil, func(c *httpapi.Config) { c.AllowedOrigins = []string{allowed}; c.AllowNoOrigin = false })
	id := e.create2("alice", map[string]string{"Origin": allowed})

	post := func(h map[string]string) reply {
		return e.do(req{method: "POST", path: "/conversations/" + id + "/messages", user: "alice",
			body: `{"message":"hi","clientMessageId":"` + fmt.Sprint(len(h)) + `x"}`, header: h})
	}
	get := func(path string, h map[string]string) reply {
		return e.do(req{method: "GET", path: path, user: "alice", header: h})
	}
	tests := []struct {
		name string
		got  reply
		want int
	}{
		{"POST from a bad origin", post(map[string]string{"Origin": "https://evil.example"}), 403},
		{"POST without Origin (AllowNoOrigin=false)", post(nil), 403},
		{"GET from a bad origin", get("/conversations/"+id, map[string]string{"Origin": "https://evil.example"}), 403},
		{"GET events from a bad origin", get("/conversations/"+id+"/events", map[string]string{"Origin": "null"}), 403},
		{"GET export from a bad origin", get("/conversations/"+id+"/export", map[string]string{"Origin": "https://evil.example"}), 403},
		{"cross-site GET without Origin", get("/conversations/"+id, map[string]string{"Sec-Fetch-Site": "cross-site"}), 403},
		{"same-origin GET without Origin", get("/conversations/"+id, nil), 200},
		{"same-site GET without Origin", get("/conversations/"+id, map[string]string{"Sec-Fetch-Site": "same-origin"}), 200},
		{"GET from the allowed origin", get("/conversations/"+id, map[string]string{"Origin": allowed}), 200},
		{"POST from the allowed origin", post(map[string]string{"Origin": allowed}), 202},
	}
	for _, tt := range tests {
		if tt.got.status != tt.want {
			t.Errorf("%s: status %d, want %d (%s)", tt.name, tt.got.status, tt.want, tt.got.body)
		}
		if tt.want == 403 && tt.got.errCode(t) != "origin_not_allowed" {
			t.Errorf("%s: code %q", tt.name, tt.got.errCode(t))
		}
	}
}

func TestOriginDefaultsToSameHost(t *testing.T) {
	e := newEnv(t, nil, func(c *httpapi.Config) { c.AllowNoOrigin = false })
	u, _ := url.Parse(e.srv.URL)
	own := map[string]string{"Origin": "http://" + u.Host}
	e.create2("alice", own) // creates fine from its own origin
	r := e.do(req{method: "POST", path: "/conversations", user: "alice", body: `{"profile":"antigravity"}`, header: map[string]string{"Origin": "http://evil.example"}})
	if r.status != http.StatusForbidden {
		t.Errorf("foreign origin with no allowlist: %d %s", r.status, r.body)
	}
}

// create2 is create with extra headers.
func (e *env) create2(user string, h map[string]string) string {
	e.t.Helper()
	r := e.do(req{method: "POST", path: "/conversations", user: user, body: `{"profile":"antigravity"}`, header: h})
	if r.status != http.StatusCreated {
		e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	var c struct{ ID string }
	r.json(e.t, &c)
	return c.ID
}

func TestRequestSizeAndRate(t *testing.T) {
	e := newEnv(t, nil, func(c *httpapi.Config) { c.MaxBodyBytes = 512; c.SendsPerMinute = 3 })
	id := e.create("alice")

	big := e.do(req{method: "POST", path: "/conversations/" + id + "/messages", user: "alice",
		body: `{"message":"` + strings.Repeat("a", 2000) + `","clientMessageId":"m1"}`})
	if big.status != http.StatusRequestEntityTooLarge || big.errCode(t) != "message_too_large" {
		t.Errorf("oversize body: %d %s", big.status, big.body)
	}

	var got []int
	for i := 0; i < 5; i++ {
		got = append(got, e.send("alice", id, "hi", fmt.Sprintf("r%d", i)).status)
	}
	// The first send starts a turn; the following ones are 409 (turn running) or 202, but the
	// rate limiter (3 per minute, and the oversize attempt above does not count: it was
	// rejected while reading the body) must cut in by the fourth.
	limited := 0
	for _, s := range got {
		if s == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Errorf("statuses %v: no request was rate limited", got)
	}
	r := e.send("alice", id, "hi", "again")
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" || r.errCode(t) != "rate_limited" {
		t.Errorf("limited response: %d %v %s", r.status, r.header, r.body)
	}
	// Another principal has its own budget.
	other := e.create("bob")
	if s := e.send("bob", other, "hi", "m1").status; s != http.StatusAccepted {
		t.Errorf("bob's first send: %d", s)
	}
}

func TestQueryAndFieldBounds(t *testing.T) {
	e := newEnv(t, nil)
	id := e.create("alice")
	ev := "/conversations/" + id + "/events"
	for _, tt := range []struct{ path, code string }{
		{ev + "?limit=0", "invalid_query"},
		{ev + "?limit=501", "invalid_query"},
		{ev + "?limit=-1", "invalid_query"},
		{ev + "?limit=abc", "invalid_query"},
		{ev + "?after=-1", "invalid_query"},
		{ev + "?after=1.5", "invalid_query"},
		{ev + "?after=99999999999999999999999", "invalid_query"},
		{"/conversations?limit=0", "invalid_query"},
		{"/conversations?limit=101", "invalid_query"},
		{"/conversations?cursor=%25%25garbage", "invalid_query"},
		{"/conversations/" + id + "/export?format=pdf", "invalid_query"},
		{"/conversations/" + id + "/export?turnId=a%20b", "invalid_query"},
	} {
		r := e.do(req{method: "GET", path: tt.path, user: "alice"})
		if r.status != http.StatusBadRequest || r.errCode(t) != tt.code {
			t.Errorf("GET %s: %d %s", tt.path, r.status, r.body)
		}
	}
	if r := e.do(req{method: "GET", path: ev + "?limit=500&after=0", user: "alice"}); r.status != 200 {
		t.Errorf("limits at the edge rejected: %d %s", r.status, r.body)
	}

	for _, tt := range []struct {
		name, body string
	}{
		{"title of 256 characters", `{"profile":"antigravity","title":"` + strings.Repeat("é", 256) + `"}`},
		{"profile not on the allowlist", `{"profile":"claude_code"}`},
		{"profile with a path in it", `{"profile":"../../bin/sh"}`},
	} {
		r := e.do(req{method: "POST", path: "/conversations", user: "alice", body: tt.body})
		if r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", tt.name, r.status, r.body)
		}
	}
	for _, cid := range []string{strings.Repeat("a", 65), "has space", "a/b", "", "é"} {
		if r := e.send("alice", id, "hi", cid); r.status != http.StatusBadRequest || r.errCode(t) != "invalid_field" {
			t.Errorf("clientMessageId %q: %d %s", cid, r.status, r.body)
		}
	}
	if r := e.do(req{method: "GET", path: "/conversations/" + id + "/stream", user: "alice", header: map[string]string{"Last-Event-ID": "-3"}}); r.status != http.StatusBadRequest {
		t.Errorf("negative Last-Event-ID: %d", r.status)
	}
	if r := e.do(req{method: "GET", path: "/conversations/" + id + "/stream", user: "alice", header: map[string]string{"Last-Event-ID": "abc"}}); r.status != http.StatusBadRequest {
		t.Errorf("garbage Last-Event-ID: %d", r.status)
	}
}

func TestMalformedBodies(t *testing.T) {
	e := newEnv(t, nil)
	id := e.create("alice")
	path := "/conversations/" + id + "/messages"
	for _, tt := range []struct {
		name, body string
		ct         string
		status     int
	}{
		{"not JSON", `hello`, "application/json", 400},
		{"trailing value", `{"message":"a","clientMessageId":"m1"}{"x":1}`, "application/json", 400},
		{"trailing garbage", `{"message":"a","clientMessageId":"m1"} nope`, "application/json", 400},
		{"array instead of object", `[1,2]`, "application/json", 400},
		{"form content type (CSRF-style request)", `message=a&clientMessageId=m1`, "application/x-www-form-urlencoded", 415},
		{"text/plain (CSRF-style request)", `{"message":"a","clientMessageId":"m1"}`, "text/plain", 415},
		{"json with charset is fine", `{"message":"a","clientMessageId":"ok1"}`, "application/json; charset=utf-8", 202},
	} {
		r := e.do(req{method: "POST", path: path, user: "alice", body: tt.body, header: map[string]string{"Content-Type": tt.ct}})
		if r.status != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.name, r.status, r.body, tt.status)
		}
	}
}

func TestMalformedPathIDsAre404(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{
		"/conversations/" + strings.Repeat("a", 129),
		"/conversations/a%20b",
		"/conversations/%2e%2e",
		"/conversations/a.b",
	} {
		r := e.do(req{method: "GET", path: p, user: "alice"})
		if r.status != http.StatusNotFound {
			t.Errorf("GET %s: %d %s", p, r.status, r.body)
		}
	}
}

// The CLI session ID, the host paths the resolver chose and the owner must never appear in a
// response, whichever endpoint shows the conversation.
func TestResponsesNeverLeakInternals(t *testing.T) {
	e := newEnv(t, []*sandboxResult{fixture(t, "agy-1.4.8-basic.jsonl")})
	id := e.create("alice")
	stream := e.open("alice", id, "")
	r := e.send("alice", id, "go", "m1")
	var turn struct{ ID string }
	r.json(t, &turn)
	bodies := []string{r.body}
	for _, ev := range stream.until("turn.completed") {
		bodies = append(bodies, ev.Data)
	}
	e.settle("alice", id)
	for _, p := range []string{
		"/conversations", "/conversations/" + id, "/conversations/" + id + "/events",
		"/conversations/" + id + "/export?format=json", "/conversations/" + id + "/export?format=md",
	} {
		bodies = append(bodies, e.do(req{method: "GET", path: p, user: "alice"}).body)
	}
	for _, leak := range []string{sessionID, e.workspace, "sessionId", "SessionID", "owner", `"alice"`, "credential"} {
		for i, b := range bodies {
			if strings.Contains(b, leak) {
				t.Errorf("response %d leaks %q:\n%s", i, leak, b)
			}
		}
	}
}

func TestErrorsDoNotRevealInternals(t *testing.T) {
	// An unexpected failure must produce a generic message.
	e := newEnv(t, nil)
	r := e.do(req{method: "POST", path: "/conversations", user: "alice", body: `{"profile":"antigravity"}`})
	if r.status != http.StatusCreated {
		t.Fatal(r.body)
	}
	if got := e.do(req{method: "GET", path: "/conversations/nope", user: "alice"}); strings.Contains(got.body, "memstore") || strings.Contains(got.body, "conversation:") {
		t.Errorf("error body mentions internals: %s", got.body)
	}
}
