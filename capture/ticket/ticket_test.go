package ticket

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemory_MintConsume(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		advance   time.Duration
		scope     string
		consumes  int
		wantFirst bool
	}{
		{"valid", time.Second, "org-1", 1, true},
		{"expired", 21 * time.Second, "org-1", 1, false},
		{"wrong scope", time.Second, "org-2", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMemory(0)
			now := base
			m.now = func() time.Time { return now }

			token, err := m.Mint(ctx, Ticket{Subject: "u1", Scope: "org-1", Value: "claude"})
			if err != nil || token == "" {
				t.Fatalf("Mint: %q, %v", token, err)
			}
			now = base.Add(tt.advance)

			got, ok, err := m.Consume(ctx, token, tt.scope)
			if err != nil || ok != tt.wantFirst {
				t.Fatalf("Consume ok=%v err=%v, want ok=%v", ok, err, tt.wantFirst)
			}
			if ok && (got.Subject != "u1" || got.Value != "claude" || got.ExpiresAt.IsZero()) {
				t.Errorf("unexpected ticket %+v", got)
			}
			if _, again, _ := m.Consume(ctx, token, "org-1"); again {
				t.Error("a token must never be consumable twice")
			}
		})
	}
}

func TestMemory_UnknownToken(t *testing.T) {
	if _, ok, _ := NewMemory(time.Second).Consume(context.Background(), "nope", "s"); ok {
		t.Error("unknown token must not be accepted")
	}
}

func TestMemory_EvictsExpiredOnMint(t *testing.T) {
	m := NewMemory(time.Second)
	now := time.Now()
	m.now = func() time.Time { return now }
	_, _ = m.Mint(context.Background(), Ticket{Scope: "s"})
	now = now.Add(time.Minute)
	_, _ = m.Mint(context.Background(), Ticket{Scope: "s"})
	if len(m.tickets) != 1 {
		t.Errorf("expired ticket not evicted, have %d", len(m.tickets))
	}
}

func TestMemory_ConcurrentConsumeIsSingleUse(t *testing.T) {
	m := NewMemory(time.Minute)
	token, _ := m.Mint(context.Background(), Ticket{Scope: "s"})

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := m.Consume(context.Background(), token, "s"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("token consumed %d times, want exactly 1", wins)
	}
}
