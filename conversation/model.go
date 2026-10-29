package conversation

import (
	"time"

	"github.com/baby-hero-404/aicli/run/stream"
)

// Principal is the opaque identity of whoever makes a request. The host decides what it
// means; this package only passes it to the Authorizer and stores it as the owner.
type Principal string

// Action is something a principal asks to do.
type Action string

// Actions passed to Authorizer.Authorize.
const (
	// ActionCreate and ActionList have no conversation yet; the conversation argument is nil.
	ActionCreate Action = "create"
	ActionList   Action = "list"
	ActionRead   Action = "read"
	ActionSend   Action = "send"
	ActionCancel Action = "cancel"
	ActionExport Action = "export"
)

// TurnStatus is the state of one turn.
type TurnStatus string

// Turn states.
const (
	TurnRunning   TurnStatus = "running"
	TurnCompleted TurnStatus = "completed"
	TurnFailed    TurnStatus = "failed"
	TurnCancelled TurnStatus = "cancelled"
)

// Finished reports whether the turn will not change any more.
func (s TurnStatus) Finished() bool { return s != TurnRunning }

// FailureCode says why a turn failed. It is carried by the turn.failed event.
type FailureCode string

// Failure codes.
const (
	FailQuotaExceeded FailureCode = "quota_exceeded"
	FailAuthInvalid   FailureCode = "auth_invalid"
	FailAwaitingInput FailureCode = "awaiting_input"
	// FailSessionExpired: the CLI could not resume its session; the conversation is no longer
	// resumable.
	FailSessionExpired FailureCode = "session_expired"
	// FailInterrupted: the process or server went away mid-turn.
	FailInterrupted FailureCode = "interrupted"
	FailRunFailed   FailureCode = "run_failed"
)

// Conversation is a sequence of turns with one CLI profile.
type Conversation struct {
	// ID is opaque and random; it is the only identifier clients see.
	ID      string
	Owner   Principal
	Profile string
	Title   string
	// Resumable is false once the CLI session can no longer be continued. History stays
	// readable and exportable.
	Resumable bool
	// SessionID is the CLI's own conversation ID. It never leaves the server.
	SessionID string
	// ActiveTurnID is the running turn, or "" when the conversation is idle. Stores maintain
	// it atomically in ClaimTurn and FinishTurn.
	ActiveTurnID string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Running reports whether a turn is in progress.
func (c Conversation) Running() bool { return c.ActiveTurnID != "" }

// Turn is one message and the CLI run that answers it.
type Turn struct {
	ID             string
	ConversationID string
	// ClientMessageID is the caller's idempotency key, unique within a conversation.
	ClientMessageID string
	Status          TurnStatus
	// Failure is set when Status is TurnFailed.
	Failure FailureCode
	// RetryAfter is set with FailQuotaExceeded when the CLI said when quota returns.
	RetryAfter time.Duration
	// CredentialsNotSaved is set when the CLI refreshed credentials but Resolver.SaveCredentials
	// failed, so the host should alert: the next turn may fail with auth_invalid.
	CredentialsNotSaved bool
	// CancelRequested is set by Store.RequestCancel and polled by the instance running the turn.
	CancelRequested bool
	// LeaseUntil is renewed while a live process owns the turn; an expired lease marks it stale.
	LeaseUntil time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// Event is one entry of a conversation's append-only log.
type Event struct {
	ConversationID string
	// Seq is assigned by Store.AppendEvents: consecutive per conversation, starting at 1.
	Seq    int64
	TurnID string
	Time   time.Time
	Kind   stream.Kind
	Text   string
	Tool   *stream.Tool
	Meta   map[string]string
}
