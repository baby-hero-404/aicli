// Package memstore is an in-memory conversation.Store for tests and demos. It is correct for a
// single process only: nothing is persisted and nothing is shared between instances.
package memstore

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
)

// Store is an in-memory conversation.Store. The zero value is not usable; call New.
type Store struct {
	mu       sync.Mutex
	convs    map[string]*conversation.Conversation
	order    []string // conversation IDs in creation order
	turns    map[string]*conversation.Turn
	byClient map[string]string // conversation ID + "\x00" + client message ID -> turn ID
	events   map[string][]conversation.Event
}

var _ conversation.Store = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{
		convs:    map[string]*conversation.Conversation{},
		turns:    map[string]*conversation.Turn{},
		byClient: map[string]string{},
		events:   map[string][]conversation.Event{},
	}
}

// CreateConversation implements conversation.Store.
func (s *Store) CreateConversation(_ context.Context, c conversation.Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.convs[c.ID]; ok {
		return conversation.ErrInvalidArgument
	}
	c.ActiveTurnID = ""
	s.convs[c.ID] = &c
	s.order = append(s.order, c.ID)
	return nil
}

// GetConversation implements conversation.Store.
func (s *Store) GetConversation(_ context.Context, id string) (conversation.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[id]
	if !ok {
		return conversation.Conversation{}, conversation.ErrNotFound
	}
	return *c, nil
}

// ListConversations implements conversation.Store. The cursor is the number of items already
// returned.
func (s *Store) ListConversations(_ context.Context, owner conversation.Principal, cursor string, limit int) ([]conversation.Conversation, string, error) {
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", conversation.ErrInvalidCursor
		}
		offset = n
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mine []conversation.Conversation
	for _, id := range s.order {
		if c := s.convs[id]; c.Owner == owner {
			mine = append(mine, *c)
		}
	}
	slices.SortStableFunc(mine, func(a, b conversation.Conversation) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return -compareStrings(a.ID, b.ID)
	})
	if offset >= len(mine) {
		return nil, "", nil
	}
	end := len(mine)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	next := ""
	if end < len(mine) {
		next = strconv.Itoa(end)
	}
	return mine[offset:end], next, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ClaimTurn implements conversation.Store.
func (s *Store) ClaimTurn(_ context.Context, t conversation.Turn) (conversation.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[t.ConversationID]
	if !ok {
		return conversation.Turn{}, conversation.ErrNotFound
	}
	if id, ok := s.byClient[t.ConversationID+"\x00"+t.ClientMessageID]; ok {
		return *s.turns[id], conversation.ErrDuplicate
	}
	if c.ActiveTurnID != "" {
		return conversation.Turn{}, conversation.ErrTurnInProgress
	}
	t.Status = conversation.TurnRunning
	s.turns[t.ID] = &t
	s.byClient[t.ConversationID+"\x00"+t.ClientMessageID] = t.ID
	c.ActiveTurnID = t.ID
	return t, nil
}

// GetTurn implements conversation.Store.
func (s *Store) GetTurn(_ context.Context, id string) (conversation.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.turns[id]
	if !ok {
		return conversation.Turn{}, conversation.ErrNotFound
	}
	return *t, nil
}

// RenewLease implements conversation.Store.
func (s *Store) RenewLease(_ context.Context, turnID string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.turns[turnID]
	if !ok {
		return conversation.ErrNotFound
	}
	if t.Status.Finished() {
		return conversation.ErrTurnNotRunning
	}
	t.LeaseUntil = until
	return nil
}

// RequestCancel implements conversation.Store.
func (s *Store) RequestCancel(_ context.Context, turnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.turns[turnID]
	if !ok {
		return conversation.ErrNotFound
	}
	if t.Status.Finished() {
		return conversation.ErrTurnNotRunning
	}
	t.CancelRequested = true
	return nil
}

// CancelRequested implements conversation.Store.
func (s *Store) CancelRequested(_ context.Context, turnIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range turnIDs {
		if t, ok := s.turns[id]; ok && t.CancelRequested && !t.Status.Finished() {
			out = append(out, id)
		}
	}
	return out, nil
}

// FinishTurn implements conversation.Store.
func (s *Store) FinishTurn(_ context.Context, t conversation.Turn, sessionID string, resumable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.turns[t.ID]
	if !ok {
		return conversation.ErrNotFound
	}
	c := s.convs[stored.ConversationID]
	cancel := stored.CancelRequested
	*stored = t
	stored.ConversationID = c.ID
	stored.CancelRequested = cancel
	if c.ActiveTurnID == t.ID {
		c.ActiveTurnID = ""
	}
	c.Resumable = resumable
	if sessionID != "" {
		c.SessionID = sessionID
	}
	c.UpdatedAt = t.FinishedAt
	return nil
}

// StaleTurns implements conversation.Store.
func (s *Store) StaleTurns(_ context.Context, now time.Time) ([]conversation.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []conversation.Turn
	for _, t := range s.turns {
		if !t.Status.Finished() && t.LeaseUntil.Before(now) {
			out = append(out, *t)
		}
	}
	slices.SortFunc(out, func(a, b conversation.Turn) int { return compareStrings(a.ID, b.ID) })
	return out, nil
}

// AppendEvents implements conversation.Store.
func (s *Store) AppendEvents(_ context.Context, conversationID string, events []conversation.Event) ([]conversation.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.convs[conversationID]; !ok {
		return nil, conversation.ErrNotFound
	}
	log := s.events[conversationID]
	out := make([]conversation.Event, len(events))
	for i, e := range events {
		e = cloneEvent(e)
		e.ConversationID = conversationID
		e.Seq = int64(len(log) + 1)
		log = append(log, e)
		out[i] = cloneEvent(e)
	}
	s.events[conversationID] = log
	return out, nil
}

// ListEvents implements conversation.Store.
func (s *Store) ListEvents(_ context.Context, conversationID string, afterSeq int64, limit int) ([]conversation.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.convs[conversationID]; !ok {
		return nil, conversation.ErrNotFound
	}
	log := s.events[conversationID]
	if afterSeq < 0 {
		afterSeq = 0
	}
	if afterSeq >= int64(len(log)) {
		return nil, nil
	}
	rest := log[afterSeq:]
	if limit > 0 && limit < len(rest) {
		rest = rest[:limit]
	}
	out := make([]conversation.Event, len(rest))
	for i, e := range rest {
		out[i] = cloneEvent(e)
	}
	return out, nil
}

func cloneEvent(e conversation.Event) conversation.Event {
	if e.Tool != nil {
		t := *e.Tool
		e.Tool = &t
	}
	e.Meta = maps.Clone(e.Meta)
	return e
}
