// Package storetest is a contract test suite for conversation.Store implementations. Run it
// from the implementation's own tests:
//
//	func TestStore(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) conversation.Store { return mystore.New(testDB(t)) })
//	}
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/run/stream"
)

// Factory returns a new, empty store for one test.
type Factory func(t *testing.T) conversation.Store

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func conv(id, owner string, n int) conversation.Conversation {
	return conversation.Conversation{
		ID: id, Owner: conversation.Principal(owner), Profile: "antigravity", Resumable: true,
		CreatedAt: epoch.Add(time.Duration(n) * time.Minute), UpdatedAt: epoch.Add(time.Duration(n) * time.Minute),
	}
}

func turn(id, convID, client string) conversation.Turn {
	return conversation.Turn{
		ID: id, ConversationID: convID, ClientMessageID: client,
		Status: conversation.TurnRunning, StartedAt: epoch, LeaseUntil: epoch.Add(time.Hour),
	}
}

// Run executes the whole suite.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(t *testing.T, s conversation.Store)
	}{
		{"CreateAndGet", testCreateAndGet},
		{"ListFiltersByOwnerNewestFirstAndPages", testList},
		{"ListRejectsForeignCursor", testListBadCursor},
		{"ClaimTurn", testClaim},
		{"ClaimTurnIsAtomicUnderConcurrency", testClaimConcurrent},
		{"ClaimTurnIsIdempotentPerClientMessageID", testClaimIdempotent},
		{"FinishTurn", testFinish},
		{"CancelRequest", testCancel},
		{"LeaseAndStaleTurns", testStale},
		{"AppendEventsAssignsConsecutiveSeq", testAppendSeq},
		{"AppendEventsConcurrentSeqIsGapFree", testAppendConcurrent},
		{"ListEventsPagination", testListEvents},
		{"EventsAreIsolatedFromCallerMutation", testEventAliasing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.fn(t, newStore(t)) })
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testCreateAndGet(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	c := conv("c1", "alice", 0)
	c.Title, c.SessionID = "hello", "sess"
	must(t, s.CreateConversation(ctx, c))
	got, err := s.GetConversation(ctx, "c1")
	must(t, err)
	if got.ID != "c1" || got.Owner != "alice" || got.Title != "hello" || got.SessionID != "sess" || !got.Resumable || got.ActiveTurnID != "" {
		t.Errorf("GetConversation = %+v", got)
	}
	if _, err := s.GetConversation(ctx, "nope"); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown ID: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetTurn(ctx, "nope"); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown turn: err = %v, want ErrNotFound", err)
	}
}

func testList(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		must(t, s.CreateConversation(ctx, conv(fmt.Sprintf("a%d", i), "alice", i)))
	}
	must(t, s.CreateConversation(ctx, conv("b0", "bob", 10)))

	var ids []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, next, err := s.ListConversations(ctx, "alice", cursor, 2)
		must(t, err)
		if len(page) > 2 {
			t.Fatalf("page has %d items, limit was 2", len(page))
		}
		for _, c := range page {
			if c.Owner != "alice" {
				t.Errorf("listed a foreign conversation: %+v", c)
			}
			ids = append(ids, c.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	want := []string{"a4", "a3", "a2", "a1", "a0"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ids = %v, want %v (newest first, no repeats, no gaps)", ids, want)
	}
	if page, next, err := s.ListConversations(ctx, "nobody", "", 10); err != nil || len(page) != 0 || next != "" {
		t.Errorf("empty list = %v, %q, %v", page, next, err)
	}
}

func testListBadCursor(t *testing.T, s conversation.Store) {
	if _, _, err := s.ListConversations(context.Background(), "alice", "%%%not-a-cursor", 5); !errors.Is(err, conversation.ErrInvalidCursor) {
		t.Errorf("err = %v, want ErrInvalidCursor", err)
	}
}

func testClaim(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))

	got, err := s.ClaimTurn(ctx, turn("t1", "c1", "m1"))
	must(t, err)
	if got.ID != "t1" || got.Status != conversation.TurnRunning {
		t.Errorf("claimed = %+v", got)
	}
	c, _ := s.GetConversation(ctx, "c1")
	if c.ActiveTurnID != "t1" {
		t.Errorf("ActiveTurnID = %q, want t1", c.ActiveTurnID)
	}
	if _, err := s.ClaimTurn(ctx, turn("t2", "c1", "m2")); !errors.Is(err, conversation.ErrTurnInProgress) {
		t.Errorf("second claim: err = %v, want ErrTurnInProgress", err)
	}
	if _, err := s.ClaimTurn(ctx, turn("t9", "missing", "m1")); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown conversation: err = %v, want ErrNotFound", err)
	}
	stored, err := s.GetTurn(ctx, "t1")
	must(t, err)
	if stored.ConversationID != "c1" || stored.ClientMessageID != "m1" {
		t.Errorf("GetTurn = %+v", stored)
	}
}

func testClaimConcurrent(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	const n = 50
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		losers  int
		other   []error
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ClaimTurn(ctx, turn(fmt.Sprintf("t%d", i), "c1", fmt.Sprintf("m%d", i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, conversation.ErrTurnInProgress):
				losers++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 || losers != n-1 || len(other) != 0 {
		t.Errorf("winners=%d losers=%d other=%v; want exactly 1 winner", winners, losers, other)
	}
}

func testClaimIdempotent(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	first, err := s.ClaimTurn(ctx, turn("t1", "c1", "same"))
	must(t, err)
	// Same client message ID, even while the first turn is still running: the existing turn
	// comes back with ErrDuplicate instead of ErrTurnInProgress.
	dup, err := s.ClaimTurn(ctx, turn("t2", "c1", "same"))
	if !errors.Is(err, conversation.ErrDuplicate) || dup.ID != first.ID {
		t.Errorf("duplicate while running: %+v, %v", dup, err)
	}
	must(t, s.FinishTurn(ctx, withStatus(first, conversation.TurnCompleted), "", true))
	dup, err = s.ClaimTurn(ctx, turn("t3", "c1", "same"))
	if !errors.Is(err, conversation.ErrDuplicate) || dup.ID != first.ID || dup.Status != conversation.TurnCompleted {
		t.Errorf("duplicate after finish: %+v, %v", dup, err)
	}
	// The same ID in another conversation is unrelated.
	must(t, s.CreateConversation(ctx, conv("c2", "alice", 1)))
	if _, err := s.ClaimTurn(ctx, turn("t4", "c2", "same")); err != nil {
		t.Errorf("same client ID in another conversation: %v", err)
	}
}

func withStatus(t conversation.Turn, st conversation.TurnStatus) conversation.Turn {
	t.Status = st
	t.FinishedAt = epoch.Add(time.Minute)
	return t
}

func testFinish(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	c := conv("c1", "alice", 0)
	c.SessionID = "old"
	must(t, s.CreateConversation(ctx, c))
	tr, err := s.ClaimTurn(ctx, turn("t1", "c1", "m1"))
	must(t, err)

	tr.Status, tr.Failure, tr.RetryAfter, tr.CredentialsNotSaved = conversation.TurnFailed, conversation.FailQuotaExceeded, 90*time.Second, true
	tr.FinishedAt = epoch.Add(time.Minute)
	must(t, s.FinishTurn(ctx, tr, "new", false))

	got, _ := s.GetTurn(ctx, "t1")
	if got.Status != conversation.TurnFailed || got.Failure != conversation.FailQuotaExceeded || got.RetryAfter != 90*time.Second || !got.CredentialsNotSaved || got.FinishedAt.IsZero() {
		t.Errorf("turn = %+v", got)
	}
	gc, _ := s.GetConversation(ctx, "c1")
	if gc.ActiveTurnID != "" || gc.SessionID != "new" || gc.Resumable {
		t.Errorf("conversation = %+v", gc)
	}

	// An empty session ID keeps the stored one.
	tr2, err := s.ClaimTurn(ctx, turn("t2", "c1", "m2"))
	must(t, err)
	must(t, s.FinishTurn(ctx, withStatus(tr2, conversation.TurnCompleted), "", true))
	if gc, _ = s.GetConversation(ctx, "c1"); gc.SessionID != "new" || !gc.Resumable {
		t.Errorf("after empty session ID: %+v", gc)
	}
}

func testCancel(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	tr, err := s.ClaimTurn(ctx, turn("t1", "c1", "m1"))
	must(t, err)

	if got, err := s.CancelRequested(ctx, []string{"t1"}); err != nil || len(got) != 0 {
		t.Errorf("before request: %v, %v", got, err)
	}
	must(t, s.RequestCancel(ctx, "t1"))
	must(t, s.RequestCancel(ctx, "t1")) // idempotent
	if got, err := s.CancelRequested(ctx, []string{"t1", "unknown"}); err != nil || len(got) != 1 || got[0] != "t1" {
		t.Errorf("after request: %v, %v", got, err)
	}
	must(t, s.FinishTurn(ctx, withStatus(tr, conversation.TurnCancelled), "", true))
	if err := s.RequestCancel(ctx, "t1"); !errors.Is(err, conversation.ErrTurnNotRunning) {
		t.Errorf("finished turn: err = %v, want ErrTurnNotRunning", err)
	}
	if err := s.RequestCancel(ctx, "unknown"); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown turn: err = %v, want ErrNotFound", err)
	}
}

func testStale(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	for _, id := range []string{"c1", "c2", "c3"} {
		must(t, s.CreateConversation(ctx, conv(id, "alice", 0)))
	}
	expired := turn("expired", "c1", "m")
	expired.LeaseUntil = epoch.Add(time.Minute)
	renewed := turn("renewed", "c2", "m")
	renewed.LeaseUntil = epoch.Add(time.Minute)
	done := turn("done", "c3", "m")
	done.LeaseUntil = epoch.Add(time.Minute)
	for _, tr := range []conversation.Turn{expired, renewed, done} {
		if _, err := s.ClaimTurn(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	must(t, s.RenewLease(ctx, "renewed", epoch.Add(time.Hour)))
	must(t, s.FinishTurn(ctx, withStatus(done, conversation.TurnCompleted), "", true))

	stale, err := s.StaleTurns(ctx, epoch.Add(10*time.Minute))
	must(t, err)
	if len(stale) != 1 || stale[0].ID != "expired" {
		t.Errorf("StaleTurns = %+v, want only the expired running turn", stale)
	}
}

func ev(turnID, text string) conversation.Event {
	return conversation.Event{TurnID: turnID, Time: epoch, Kind: stream.KindAssistantText, Text: text}
}

func testAppendSeq(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	must(t, s.CreateConversation(ctx, conv("c2", "alice", 0)))

	got, err := s.AppendEvents(ctx, "c1", []conversation.Event{ev("t", "a"), ev("t", "b")})
	must(t, err)
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || got[0].ConversationID != "c1" {
		t.Errorf("first append = %+v", got)
	}
	got, err = s.AppendEvents(ctx, "c1", []conversation.Event{ev("t", "c")})
	must(t, err)
	if got[0].Seq != 3 {
		t.Errorf("second append seq = %d, want 3", got[0].Seq)
	}
	other, err := s.AppendEvents(ctx, "c2", []conversation.Event{ev("t", "x")})
	must(t, err)
	if other[0].Seq != 1 {
		t.Errorf("sequences are per conversation; c2 first seq = %d", other[0].Seq)
	}
	if _, err := s.AppendEvents(ctx, "missing", []conversation.Event{ev("t", "x")}); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown conversation: err = %v", err)
	}
}

func testAppendConcurrent(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	const workers, per = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := s.AppendEvents(ctx, "c1", []conversation.Event{ev("t", "x"), ev("t", "y")}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	all, err := s.ListEvents(ctx, "c1", 0, 10000)
	must(t, err)
	if len(all) != workers*per*2 {
		t.Fatalf("stored %d events, want %d", len(all), workers*per*2)
	}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d: sequence has a gap or is out of order", i, e.Seq)
		}
	}
}

func testListEvents(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	var batch []conversation.Event
	for i := 1; i <= 10; i++ {
		batch = append(batch, ev("t", fmt.Sprint(i)))
	}
	_, err := s.AppendEvents(ctx, "c1", batch)
	must(t, err)

	tests := []struct {
		after int64
		limit int
		first int64
		n     int
	}{
		{0, 100, 1, 10},
		{0, 3, 1, 3},
		{3, 4, 4, 4},
		{8, 100, 9, 2},
		{10, 100, 0, 0},
		{99, 100, 0, 0},
	}
	for _, tt := range tests {
		got, err := s.ListEvents(ctx, "c1", tt.after, tt.limit)
		must(t, err)
		if len(got) != tt.n || (tt.n > 0 && got[0].Seq != tt.first) {
			t.Errorf("ListEvents(after=%d, limit=%d) = %d events starting at %v; want %d starting at %d", tt.after, tt.limit, len(got), firstSeq(got), tt.n, tt.first)
		}
	}
	if got, err := s.ListEvents(ctx, "missing", 0, 10); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("unknown conversation: %v, %v", got, err)
	}
}

func firstSeq(evs []conversation.Event) any {
	if len(evs) == 0 {
		return "-"
	}
	return evs[0].Seq
}

func testEventAliasing(t *testing.T, s conversation.Store) {
	ctx := context.Background()
	must(t, s.CreateConversation(ctx, conv("c1", "alice", 0)))
	in := ev("t", "x")
	in.Tool = &stream.Tool{Name: "n", Summary: "s"}
	in.Meta = map[string]string{"k": "v"}
	_, err := s.AppendEvents(ctx, "c1", []conversation.Event{in})
	must(t, err)

	in.Tool.Summary = "mutated"
	in.Meta["k"] = "mutated"
	got, _ := s.ListEvents(ctx, "c1", 0, 10)
	if got[0].Tool.Summary != "s" || got[0].Meta["k"] != "v" {
		t.Errorf("stored event changed after the caller mutated its input: %+v", got[0])
	}
	got[0].Tool.Summary = "mutated again"
	again, _ := s.ListEvents(ctx, "c1", 0, 10)
	if again[0].Tool.Summary != "s" {
		t.Error("stored event changed after the caller mutated a returned event")
	}
}
