package conversation

import (
	"context"
	"time"
)

// Store is the durable state behind conversations. The host implements it on its own database;
// memstore is a reference implementation for tests. The contract is checked by storetest.Run,
// which every implementation should run.
//
// All methods must be safe for concurrent use, including from several backend instances
// sharing one database: ClaimTurn in particular must be atomic across instances.
type Store interface {
	CreateConversation(ctx context.Context, c Conversation) error
	// GetConversation returns ErrNotFound for an unknown ID.
	GetConversation(ctx context.Context, id string) (Conversation, error)
	// ListConversations returns the owner's conversations, newest first, and the cursor for the
	// next page ("" when there is none). A cursor the store did not issue is ErrInvalidCursor.
	ListConversations(ctx context.Context, owner Principal, cursor string, limit int) ([]Conversation, string, error)

	// ClaimTurn atomically records t as running and makes it the conversation's ActiveTurnID.
	// It returns ErrNotFound for an unknown conversation; ErrTurnInProgress when the
	// conversation already has an active turn; and the already stored turn with ErrDuplicate
	// when t.ClientMessageID was used before in that conversation (checked first).
	ClaimTurn(ctx context.Context, t Turn) (Turn, error)
	// GetTurn returns ErrNotFound for an unknown ID.
	GetTurn(ctx context.Context, id string) (Turn, error)
	// RenewLease extends a running turn's lease.
	RenewLease(ctx context.Context, turnID string, until time.Time) error
	// RequestCancel marks a running turn for cancellation. It is idempotent and returns
	// ErrTurnNotRunning when the turn is finished.
	RequestCancel(ctx context.Context, turnID string) error
	// CancelRequested returns those of turnIDs that are marked for cancellation.
	CancelRequested(ctx context.Context, turnIDs []string) ([]string, error)
	// FinishTurn stores the final state of t (status, failure, finish time, flags), clears the
	// conversation's ActiveTurnID and sets its Resumable flag; a non-empty sessionID replaces
	// the stored CLI session ID. It is atomic.
	FinishTurn(ctx context.Context, t Turn, sessionID string, resumable bool) error
	// StaleTurns returns running turns whose lease ended before now.
	StaleTurns(ctx context.Context, now time.Time) ([]Turn, error)

	// AppendEvents appends events to the conversation's log and returns them with Seq
	// assigned: consecutive values starting at 1, in the given order, even when several
	// appends run concurrently.
	AppendEvents(ctx context.Context, conversationID string, events []Event) ([]Event, error)
	// ListEvents returns events with Seq > afterSeq in order, at most limit of them.
	ListEvents(ctx context.Context, conversationID string, afterSeq int64, limit int) ([]Event, error)
}
