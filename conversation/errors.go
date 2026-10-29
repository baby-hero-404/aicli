package conversation

import "errors"

// Errors returned by the Service and by Store implementations. Wrap with %w to add detail.
var (
	ErrNotFound          = errors.New("conversation: not found")
	ErrForbidden         = errors.New("conversation: forbidden")
	ErrTurnInProgress    = errors.New("conversation: a turn is already running")
	ErrTurnNotRunning    = errors.New("conversation: turn is not running")
	ErrNotResumable      = errors.New("conversation: not resumable")
	ErrProfileNotAllowed = errors.New("conversation: profile not allowed")
	ErrTooManyTurns      = errors.New("conversation: too many active turns")
	// ErrInvalidArgument marks bad caller input (size, format, range). The wrapped text says what.
	ErrInvalidArgument = errors.New("conversation: invalid argument")
	// ErrMessageTooLarge is an ErrInvalidArgument for an oversize message.
	ErrMessageTooLarge = errors.New("conversation: message too large")
	// ErrInvalidCursor: the pagination cursor is not one the store issued.
	ErrInvalidCursor = errors.New("conversation: invalid cursor")
	// ErrDuplicate is returned by Store.ClaimTurn together with the existing turn when the
	// ClientMessageID was already used in that conversation. It is not a failure.
	ErrDuplicate = errors.New("conversation: duplicate client message id")
)
