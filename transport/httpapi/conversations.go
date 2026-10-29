package httpapi

import (
	"net/http"

	"github.com/baby-hero-404/aicli/conversation"
)

func (a *api) create(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	var req createRequest
	if !a.decode(w, r, &req) {
		return
	}
	c, err := a.svc.Create(r.Context(), p, req.Profile, req.Title)
	if err != nil {
		a.serviceError(w, err)
		return
	}
	a.json(w, http.StatusCreated, toConversation(c))
}

func (a *api) list(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	limit, ok := a.intParam(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	items, next, err := a.svc.List(r.Context(), p, r.URL.Query().Get("cursor"), int(limit))
	if err != nil {
		a.serviceError(w, err)
		return
	}
	out := make([]conversationDTO, len(items))
	for i, c := range items {
		out[i] = toConversation(c)
	}
	a.json(w, http.StatusOK, struct {
		Items      []conversationDTO `json:"items"`
		NextCursor string            `json:"nextCursor,omitempty"`
	}{out, next})
}

func (a *api) get(w http.ResponseWriter, r *http.Request, p conversation.Principal) {
	id, ok := a.pathID(w, r, "id")
	if !ok {
		return
	}
	c, err := a.svc.Get(r.Context(), p, id)
	if err != nil {
		a.serviceError(w, err)
		return
	}
	a.json(w, http.StatusOK, toConversation(c))
}
