package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
)

func (a *api) events(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	after, ok := a.intParam(w, r, "after", 0, 0, 1<<62)
	if !ok {
		return
	}
	limit, ok := a.intParam(w, r, "limit", 200, 1, 500)
	if !ok {
		return
	}
	evs, err := a.svc.Events(r.Context(), p, id, after, int(limit))
	if err != nil {
		a.serviceError(w, err)
		return
	}
	out := make([]eventDTO, len(evs))
	for i, e := range evs {
		out[i] = toEvent(e)
	}
	a.json(w, http.StatusOK, struct {
		Items []eventDTO `json:"items"`
	}{out})
}

// stream serves the conversation's events as Server-Sent Events. One stream per open
// conversation: it stays open across turns and sends a comment line every Heartbeat while idle.
// A reconnecting client sends Last-Event-ID and receives everything after it.
func (a *api) stream(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	var last int64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			a.fail(w, http.StatusBadRequest, "invalid_query", "Last-Event-ID must be a non-negative integer")
			return
		}
		last = n
	}
	rc := http.NewResponseController(w)
	if !a.streams.acquire(p) {
		w.Header().Set("Retry-After", "5")
		a.fail(w, http.StatusTooManyRequests, "rate_limited", "too many open streams")
		return
	}
	defer a.streams.release(p)

	ch, err := a.svc.Subscribe(r.Context(), p, id, last)
	if err != nil {
		a.serviceError(w, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store, no-transform")
	h.Set("X-Accel-Buffering", "no") // do not let a reverse proxy buffer the stream
	w.WriteHeader(http.StatusOK)
	// A long-lived response must outlive the server's write timeout, if it has one.
	_ = rc.SetWriteDeadline(time.Time{})
	if _, err := fmt.Fprint(w, ": ok\n\n"); err != nil || rc.Flush() != nil {
		return
	}

	beat := time.NewTicker(a.cfg.Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case e, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(toEvent(e)) // JSON never contains a raw newline
			if err != nil {
				a.log.Error("httpapi: encode event", "err", err)
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, data); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

var exportTurnID = idPattern

func (a *api) export(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = conversation.FormatMarkdown
	}
	if format != conversation.FormatMarkdown && format != conversation.FormatJSON {
		a.fail(w, http.StatusBadRequest, "invalid_query", "format must be md or json")
		return
	}
	turnID := r.URL.Query().Get("turnId")
	if turnID != "" && !exportTurnID.MatchString(turnID) {
		a.fail(w, http.StatusBadRequest, "invalid_query", "invalid turnId")
		return
	}
	out, err := a.svc.Export(r.Context(), p, id, format, turnID)
	if err != nil {
		a.serviceError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", out.ContentType)
	h.Set("Content-Disposition", `attachment; filename="`+out.Filename+`"`)
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(out.Data)
}
