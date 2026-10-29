package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
)

func (a *api) send(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	var req sendRequest
	if !a.decode(w, r, &req) {
		return
	}
	if ok, wait := a.sends.allow(p, a.cfg.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		a.fail(w, http.StatusTooManyRequests, "rate_limited", "too many messages, slow down")
		return
	}
	t, duplicate, err := a.svc.Send(r.Context(), p, id, req.Message, req.ClientMessageID)
	if err != nil {
		a.serviceError(w, err)
		return
	}
	status := http.StatusAccepted
	if duplicate {
		status = http.StatusOK
	}
	a.json(w, status, toTurn(t))
}

func (a *api) cancel(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	turnID, ok := a.pathID(w, r, "turnID")
	if !ok {
		return
	}
	if err := a.svc.Cancel(r.Context(), p, id, turnID); err != nil {
		a.serviceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
