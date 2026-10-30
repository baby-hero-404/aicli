package wsgorilla

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/baby-hero-404/aicli/capture"
	"github.com/baby-hero-404/aicli/capture/ticket"
	"github.com/gorilla/websocket"
)

// DefaultSessionTimeout is the longest a terminal session runs when Handler.SessionTimeout is unset.
const DefaultSessionTimeout = 30 * time.Minute

// Handler serves a ticket-gated terminal session over WebSocket. It is framework-free: call
// ServeTerminal from any router, passing the tenant scope the route belongs to.
type Handler struct {
	// Tickets validates the single-use ticket carried in the "ticket" query parameter.
	Tickets ticket.Store
	// Session runs the sandboxed shell.
	Session *capture.Session
	// AllowedOrigins lists browser origins (scheme://host[:port]) allowed to connect. When
	// empty, only same-origin requests are accepted. Requests without an Origin header
	// (non-browser clients) are always allowed; they still need a valid ticket.
	AllowedOrigins []string
	// SessionTimeout caps how long one session may run (default 30 minutes), so an abandoned
	// browser tab cannot hold a container indefinitely.
	SessionTimeout time.Duration
	// Banner optionally returns text shown in the terminal before the shell starts (after Seed
	// succeeded).
	Banner func(t ticket.Ticket) string
	// OnComplete is called after a session ends, before the exit frame is sent. Use it to
	// persist the collected files server-side.
	OnComplete func(t ticket.Ticket, res capture.Result)
	// Seed, if set, returns files to place in the session HOME before the shell starts (paths
	// relative to HOME), decided from the ticket: a session that tests a stored login. An error
	// ends the session; its text is sent to the client, so keep it free of secrets.
	Seed func(ctx context.Context, t ticket.Ticket) (map[string]string, error)
	// DiscardFiles drops the collected files: the exit frame carries none and OnComplete gets
	// none. Set it with Seed, whose files are secrets that must not travel back to the browser.
	DiscardFiles bool
}

// ServeTerminal consumes the request's ticket for scope, upgrades to WebSocket and runs one
// session. It writes the HTTP error itself when the ticket is missing, invalid or expired.
func (h *Handler) ServeTerminal(w http.ResponseWriter, r *http.Request, scope string) {
	if h.Tickets == nil || h.Session == nil || h.Session.Runtime == nil {
		http.Error(w, "terminal handler is not configured", http.StatusInternalServerError)
		return
	}
	token := r.URL.Query().Get("ticket")
	if token == "" {
		http.Error(w, "missing ticket", http.StatusUnauthorized)
		return
	}
	t, ok, err := h.Tickets.Consume(r.Context(), token, scope)
	if err != nil {
		http.Error(w, "ticket store unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid, expired, or already-used ticket", http.StatusUnauthorized)
		return
	}

	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return OriginAllowed(r, h.AllowedOrigins) }}
	ws, err := up.Upgrade(w, r, nil) // on failure Upgrade has already replied
	if err != nil {
		return
	}
	conn := NewConn(ws)
	defer conn.Close()

	timeout := h.SessionTimeout
	if timeout <= 0 {
		timeout = DefaultSessionTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	conn.OnDisconnect(cancel)

	// Seed first, so a banner such as "workspace prepared" is never shown for a session that
	// could not be prepared.
	var opts capture.RunOptions
	if h.Seed != nil {
		files, err := h.Seed(ctx, t)
		if err != nil {
			conn.SendError("%v", err)
			return
		}
		opts.Files = files
	}
	if h.Banner != nil {
		conn.SendStdout("%s", h.Banner(t))
	}
	res, err := h.Session.RunWith(ctx, conn, opts)
	if err != nil {
		conn.SendError("%v", err)
		return
	}
	if h.DiscardFiles {
		res.Files = nil
	}
	if h.OnComplete != nil {
		h.OnComplete(t, res)
	}
	conn.SendStdout("\r\nSession ended. Packaging credential...\r\n")
	_ = conn.SendExit(res.Files)
}

// OriginAllowed reports whether a WebSocket handshake from r's Origin may proceed.
func OriginAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(allowed) > 0 {
		return slices.Contains(allowed, origin)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
