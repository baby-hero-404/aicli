package wsgorilla

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/capture"
	"github.com/baby-hero-404/aicli/capture/ticket"
	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/gorilla/websocket"
)

// shellRuntime simulates a sandbox PTY: it echoes stdin to stdout, records resize events,
// and leaves a credential file in the workspace, ending when stdin closes.
type shellRuntime struct {
	sandbox.StubRuntime
	resized chan sandbox.TerminalSize
}

func (s *shellRuntime) RunInteractive(_ context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, _ io.Writer) error {
	go func() {
		for sz := range req.ResizeCh {
			s.resized <- sz
		}
	}()
	buf := make([]byte, 64)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			_, _ = stdout.Write(append([]byte("echo:"), buf[:n]...))
			if strings.TrimSpace(string(buf[:n])) == "login" {
				_ = os.WriteFile(filepath.Join(req.Workspace, "token.json"), []byte(`{"ok":true}`), 0o600)
				return nil
			}
		}
		if err != nil {
			return nil
		}
	}
}

func newServer(t *testing.T, mutate func(*Handler)) (*httptest.Server, *Handler, ticket.Store, *shellRuntime) {
	t.Helper()
	rt := &shellRuntime{resized: make(chan sandbox.TerminalSize, 4)}
	store := ticket.NewMemory(time.Minute)
	h := &Handler{Tickets: store, Session: &capture.Session{Runtime: rt}}
	if mutate != nil {
		mutate(h)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeTerminal(w, r, "org-1")
	}))
	t.Cleanup(srv.Close)
	return srv, h, store, rt
}

func dial(t *testing.T, srv *httptest.Server, token string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/terminal?ticket=" + token
	return websocket.DefaultDialer.Dial(u, hdr)
}

func TestServeTerminal_RejectsBadTickets(t *testing.T) {
	srv, _, store, _ := newServer(t, nil)
	wrongScope, _ := store.Mint(context.Background(), ticket.Ticket{Scope: "org-2"})

	tests := []struct{ name, token string }{
		{"missing", ""},
		{"unknown", "deadbeef"},
		{"wrong scope", wrongScope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, resp, err := dial(t, srv, tt.token, nil)
			if err == nil {
				t.Fatal("expected handshake failure")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("response = %v, want 401", resp)
			}
		})
	}
}

func TestServeTerminal_FullSession(t *testing.T) {
	var completed ticket.Ticket
	srv, _, store, rt := newServer(t, func(h *Handler) {
		h.Banner = func(tk ticket.Ticket) string { return "hello " + tk.Value + "\r\n" }
		h.OnComplete = func(tk ticket.Ticket, _ capture.Result) { completed = tk }
	})
	token, _ := store.Mint(context.Background(), ticket.Ticket{Subject: "u1", Scope: "org-1", Value: "claude"})

	conn, _, err := dial(t, srv, token, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	_ = conn.WriteMessage(websocket.TextMessage, []byte(ResizePrefix+"120:40"))
	select {
	case sz := <-rt.resized:
		if sz.Cols != 120 || sz.Rows != 40 {
			t.Errorf("resize = %+v, want 120x40", sz)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resize frame never reached the runtime")
	}
	_ = conn.WriteMessage(websocket.TextMessage, []byte("login\n"))

	var stdout strings.Builder
	var exit map[string]any
	for exit == nil {
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read frame: %v (stdout so far %q)", err, stdout.String())
		}
		switch frame["type"] {
		case "stdout":
			stdout.WriteString(frame["data"].(string))
		case "exit":
			exit = frame
		case "error":
			t.Fatalf("session error: %v", frame["message"])
		}
	}
	if !strings.Contains(stdout.String(), "hello claude") || !strings.Contains(stdout.String(), "echo:login") {
		t.Errorf("stdout = %q", stdout.String())
	}
	payload, _ := json.Marshal(exit["payload"])
	if string(payload) != `{"token.json":"{\"ok\":true}"}` {
		t.Errorf("exit payload = %s", payload)
	}
	if completed.Subject != "u1" || completed.Value != "claude" {
		t.Errorf("OnComplete ticket = %+v", completed)
	}

	// Closing handshake: the next read must report a clean close, not a dropped connection.
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Errorf("expected normal closure, got %v", err)
	}

	// Single-use: the same token cannot open a second session.
	if _, resp, err := dial(t, srv, token, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ticket reuse must be rejected, err=%v resp=%v", err, resp)
	}
}

func TestOriginAllowed(t *testing.T) {
	req := func(origin, host string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/terminal", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	tests := []struct {
		name    string
		origin  string
		host    string
		allowed []string
		want    bool
	}{
		{"no origin header (non-browser)", "", "api.test", nil, true},
		{"same origin", "https://api.test", "api.test", nil, true},
		{"cross origin denied by default", "https://evil.test", "api.test", nil, false},
		{"allowlisted", "https://app.test", "api.test", []string{"https://app.test"}, true},
		{"not in allowlist", "https://evil.test", "api.test", []string{"https://app.test"}, false},
		{"same-origin ignored when allowlist set", "https://api.test", "api.test", []string{"https://app.test"}, false},
		{"malformed origin", "://bad", "api.test", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OriginAllowed(req(tt.origin, tt.host), tt.allowed); got != tt.want {
				t.Errorf("OriginAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServeTerminal_DeniedOriginIsRejected(t *testing.T) {
	srv, _, store, _ := newServer(t, nil)
	token, _ := store.Mint(context.Background(), ticket.Ticket{Scope: "org-1"})
	hdr := http.Header{"Origin": []string{"https://evil.test"}}
	_, resp, err := dial(t, srv, token, hdr)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin handshake must be refused with 403, err=%v resp=%v", err, resp)
	}
}

// holdRuntime behaves like a real container: it keeps running regardless of stdin and only
// stops when its context is cancelled.
type holdRuntime struct {
	sandbox.StubRuntime
	started   chan struct{}
	cancelled chan struct{}
}

func (h *holdRuntime) RunInteractive(ctx context.Context, _ sandbox.CommandRequest, stdin io.Reader, _, _ io.Writer) error {
	go func() { _, _ = io.Copy(io.Discard, stdin) }()
	close(h.started)
	<-ctx.Done()
	close(h.cancelled)
	return ctx.Err()
}

func holdServer(t *testing.T, timeout time.Duration) (*httptest.Server, ticket.Store, *holdRuntime) {
	t.Helper()
	rt := &holdRuntime{started: make(chan struct{}), cancelled: make(chan struct{})}
	store := ticket.NewMemory(time.Minute)
	h := &Handler{Tickets: store, Session: &capture.Session{Runtime: rt}, SessionTimeout: timeout}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeTerminal(w, r, "org-1") }))
	t.Cleanup(srv.Close)
	return srv, store, rt
}

func TestServeTerminal_ClientDisconnectCancelsSession(t *testing.T) {
	srv, store, rt := holdServer(t, time.Minute)
	token, _ := store.Mint(context.Background(), ticket.Ticket{Scope: "org-1"})
	conn, _, err := dial(t, srv, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-rt.started
	_ = conn.Close() // browser tab closed

	select {
	case <-rt.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("session context was not cancelled after the client disconnected: the container would leak")
	}
}

func TestServeTerminal_SessionTimeout(t *testing.T) {
	srv, store, rt := holdServer(t, 300*time.Millisecond)
	token, _ := store.Mint(context.Background(), ticket.Ticket{Scope: "org-1"})
	conn, _, err := dial(t, srv, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-rt.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop at SessionTimeout")
	}
}

func TestServeTerminal_OversizedFrameDropsConnection(t *testing.T) {
	srv, store, rt := holdServer(t, time.Minute)
	token, _ := store.Mint(context.Background(), ticket.Ticket{Scope: "org-1"})
	conn, _, err := dial(t, srv, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-rt.started
	_ = conn.WriteMessage(websocket.TextMessage, make([]byte, MaxFrameBytes+1))

	select {
	case <-rt.cancelled: // the server closed the session when the frame exceeded the limit
	case <-time.After(5 * time.Second):
		t.Fatal("an oversized frame must end the session")
	}
}

func TestServeTerminal_MisconfiguredHandler(t *testing.T) {
	for name, h := range map[string]*Handler{
		"no tickets": {Session: &capture.Session{Runtime: &sandbox.StubRuntime{}}},
		"no session": {Tickets: ticket.NewMemory(0)},
		"no runtime": {Tickets: ticket.NewMemory(0), Session: &capture.Session{}},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeTerminal(w, httptest.NewRequest(http.MethodGet, "/terminal?ticket=x", nil), "s")
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 instead of a panic", w.Code)
			}
		})
	}
}
