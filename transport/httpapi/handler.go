package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
)

// Config configures the handler.
type Config struct {
	// Service runs the conversations. Required.
	Service *conversation.Service
	// Authenticate identifies the caller from the request (cookie, bearer token, ...) or
	// returns an error, which becomes 401. Required. The principal it returns is passed to
	// the Service's Authorizer.
	Authenticate func(*http.Request) (conversation.Principal, error)

	// AllowedOrigins are the exact Origin values browsers may call from (for example
	// "https://app.example.com"). Empty means same-origin only: the Origin's host must equal
	// the request's Host.
	AllowedOrigins []string
	// AllowNoOrigin lets POST requests without an Origin header through, for non-browser
	// clients. Default false.
	AllowNoOrigin bool

	// MaxBodyBytes caps request bodies (default 64 KiB).
	MaxBodyBytes int64
	// SendsPerMinute caps messages per principal (default 10).
	SendsPerMinute int
	// MaxStreamsPerPrincipal caps simultaneously open event streams (default 5).
	MaxStreamsPerPrincipal int
	// Heartbeat is how often an idle stream sends a comment line (default 15s).
	Heartbeat time.Duration

	// Logger receives server-side errors only (never request bodies). Defaults to discarding.
	Logger *slog.Logger
	// Now is for tests.
	Now func() time.Time
}

type api struct {
	cfg     Config
	svc     *conversation.Service
	log     *slog.Logger
	sends   *window
	streams *counter
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// NewHandler returns the API handler. Mount it under a prefix with http.StripPrefix.
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Service == nil {
		return nil, errors.New("httpapi: Service is required")
	}
	if cfg.Authenticate == nil {
		return nil, errors.New("httpapi: Authenticate is required")
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 64 << 10
	}
	if cfg.SendsPerMinute <= 0 {
		cfg.SendsPerMinute = 10
	}
	if cfg.MaxStreamsPerPrincipal <= 0 {
		cfg.MaxStreamsPerPrincipal = 5
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &api{
		cfg: cfg, svc: cfg.Service, log: cfg.Logger,
		sends: newWindow(cfg.SendsPerMinute, time.Minute), streams: newCounter(cfg.MaxStreamsPerPrincipal),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /conversations", a.route(a.create))
	mux.HandleFunc("GET /conversations", a.route(a.list))
	mux.HandleFunc("GET /conversations/{id}", a.route(a.get))
	mux.HandleFunc("POST /conversations/{id}/messages", a.route(a.send))
	mux.HandleFunc("POST /conversations/{id}/turns/{turnID}/cancel", a.route(a.cancel))
	mux.HandleFunc("GET /conversations/{id}/events", a.route(a.events))
	mux.HandleFunc("GET /conversations/{id}/stream", a.route(a.stream))
	mux.HandleFunc("GET /conversations/{id}/export", a.route(a.export))
	return mux, nil
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, p conversation.Principal)

// route wraps a handler with the checks every endpoint needs: origin policy, then
// authentication. Nothing reaches the Service (or the store) before both pass.
func (a *api) route(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !originAllowed(r, a.cfg.AllowedOrigins, a.cfg.AllowNoOrigin) {
			a.fail(w, http.StatusForbidden, "origin_not_allowed", "origin not allowed")
			return
		}
		p, err := a.cfg.Authenticate(r)
		if err != nil || p == "" {
			a.fail(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		h(w, r, p)
	}
}

func (a *api) fail(w http.ResponseWriter, status int, code, msg string) {
	a.json(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

func (a *api) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.log.Warn("httpapi: write response", "err", err)
	}
}

// serviceError maps a Service error to a response. Unexpected errors are logged and reported
// without detail.
func (a *api) serviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrNotFound):
		a.fail(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, conversation.ErrForbidden):
		a.fail(w, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, conversation.ErrTurnInProgress):
		a.fail(w, http.StatusConflict, "turn_in_progress", "a turn is already running in this conversation")
	case errors.Is(err, conversation.ErrTurnNotRunning):
		a.fail(w, http.StatusConflict, "turn_not_running", "the turn is not running")
	case errors.Is(err, conversation.ErrNotResumable):
		a.fail(w, http.StatusConflict, "not_resumable", "this conversation can no longer be continued")
	case errors.Is(err, conversation.ErrProfileNotAllowed):
		a.fail(w, http.StatusBadRequest, "profile_not_allowed", "profile not allowed")
	case errors.Is(err, conversation.ErrTooManyTurns):
		w.Header().Set("Retry-After", "5")
		a.fail(w, http.StatusTooManyRequests, "rate_limited", "too many active turns")
	case errors.Is(err, conversation.ErrMessageTooLarge):
		a.fail(w, http.StatusRequestEntityTooLarge, "message_too_large", "message too large")
	case errors.Is(err, conversation.ErrInvalidCursor):
		a.fail(w, http.StatusBadRequest, "invalid_query", "invalid cursor")
	case errors.Is(err, conversation.ErrInvalidArgument):
		a.fail(w, http.StatusBadRequest, "invalid_field", publicReason(err))
	default:
		a.log.Error("httpapi: internal error", "err", err)
		a.fail(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// publicReason returns the part of an ErrInvalidArgument chain meant for the client: the text
// after the sentinel's own message.
func publicReason(err error) string {
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, conversation.ErrInvalidArgument.Error()+": "); ok {
		return after
	}
	return "invalid value"
}

// decode reads a JSON body strictly: right content type, bounded size, no unknown fields, one
// value and nothing after it. It writes the error response itself and returns false on failure.
func (a *api) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		a.fail(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		if _, extra := dec.Token(); extra != io.EOF {
			err = errors.New("unexpected data after the JSON value")
		}
	}
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		a.fail(w, http.StatusRequestEntityTooLarge, "message_too_large", "request body too large")
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		a.fail(w, http.StatusBadRequest, "unknown_field", err.Error())
	default:
		a.fail(w, http.StatusBadRequest, "invalid_field", "malformed JSON body")
	}
	return false
}

// pathID returns a path parameter that looks like one of our IDs, or writes 404.
func (a *api) pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := r.PathValue(name)
	if !idPattern.MatchString(id) {
		a.fail(w, http.StatusNotFound, "not_found", "not found")
		return "", false
	}
	return id, true
}

// intParam parses an optional integer query parameter within [lo, hi]. On a bad value it
// writes 400 invalid_query and returns false.
func (a *api) intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int64) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < lo || n > hi {
		a.fail(w, http.StatusBadRequest, "invalid_query", fmt.Sprintf("%s must be an integer between %d and %d", name, lo, hi))
		return 0, false
	}
	return n, true
}
