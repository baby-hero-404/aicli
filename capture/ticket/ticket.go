// Package ticket provides single-use, short-lived tickets that authorize a WebSocket
// connection without putting the caller's bearer token in the URL.
package ticket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultTTL is how long a minted ticket stays valid.
const DefaultTTL = 20 * time.Second

// Ticket binds a connection to the identity that requested it.
type Ticket struct {
	// Subject identifies the authenticated caller (e.g. a user ID).
	Subject string
	// Scope is the tenant the ticket is valid for (e.g. an organization ID). Consume only
	// succeeds when the scope matches, so a ticket cannot be replayed against another tenant.
	Scope string
	// Value is caller-defined data bound to the ticket (e.g. the CLI provider to log in to).
	Value string
	// ExpiresAt is set by the store when the ticket is minted.
	ExpiresAt time.Time
}

// Store mints and consumes tickets. Implementations must make Consume atomic and
// single-use: a token is removed on the first Consume whether or not it was valid.
type Store interface {
	// Mint stores t (assigning ExpiresAt) and returns the opaque token for the client.
	Mint(ctx context.Context, t Ticket) (token string, err error)
	// Consume returns the ticket for token if it exists, has not expired and its scope
	// matches. The token is invalid afterwards in every case.
	Consume(ctx context.Context, token, scope string) (Ticket, bool, error)
}

// Memory is an in-process Store. It is correct for a single API replica; with several
// replicas and no sticky routing, back Store with shared storage instead.
type Memory struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	tickets map[string]Ticket
}

// NewMemory returns an in-memory Store. A non-positive ttl selects DefaultTTL.
func NewMemory(ttl time.Duration) *Memory {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Memory{ttl: ttl, now: time.Now, tickets: make(map[string]Ticket)}
}

// Mint implements Store.
func (m *Memory) Mint(_ context.Context, t Ticket) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictExpiredLocked()
	t.ExpiresAt = m.now().Add(m.ttl)
	m.tickets[token] = t
	return token, nil
}

// Consume implements Store.
func (m *Memory) Consume(_ context.Context, token, scope string) (Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, found := m.tickets[token]
	delete(m.tickets, token) // single-use: always remove, valid or not
	if !found || m.now().After(t.ExpiresAt) || t.Scope != scope {
		return Ticket{}, false, nil
	}
	return t, true, nil
}

func (m *Memory) evictExpiredLocked() {
	now := m.now()
	for k, v := range m.tickets {
		if now.After(v.ExpiresAt) {
			delete(m.tickets, k)
		}
	}
}
