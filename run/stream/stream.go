// Package stream defines the CLI-neutral event contract for live CLI output and the plumbing
// decoders share. A Decoder turns one line of a CLI's machine-readable output into zero or more
// Events; decoders for specific CLIs live in sub-packages (stream/agy). The package imports
// nothing from aicli so that both run and the decoders can depend on it.
package stream

// Kind is the type of an Event.
type Kind string

// Event kinds.
const (
	// KindUserMessage is a message the user sent (produced by the conversation layer, not by
	// decoders).
	KindUserMessage Kind = "user.message"
	// KindTurnStarted: the CLI started working on a turn.
	KindTurnStarted Kind = "turn.started"
	// KindAssistantText: a chunk of the assistant's reply; concatenating the chunks of a turn
	// gives the full reply.
	KindAssistantText Kind = "assistant.text"
	// KindToolUse: the CLI started a tool call.
	KindToolUse Kind = "tool.use"
	// KindToolResult: a tool call finished.
	KindToolResult Kind = "tool.result"
	// KindTurnCompleted: the CLI finished the turn successfully.
	KindTurnCompleted Kind = "turn.completed"
	// KindTurnFailed: the CLI reported a failed turn, or the turn failed for another reason.
	KindTurnFailed Kind = "turn.failed"
	// KindTurnCancelled: the turn was cancelled before it finished.
	KindTurnCancelled Kind = "turn.cancelled"
)

// DefaultMaxSummary is the default cap, in bytes, on Tool.Summary.
const DefaultMaxSummary = 4096

// Tool describes a tool call in a tool.use or tool.result Event.
type Tool struct {
	Name string
	// Summary is a short, truncated description: the call's parameters for tool.use, its
	// output for tool.result. It may contain anything the tool touched, so callers must redact
	// it before it leaves the server.
	Summary string
	IsError bool
}

// Event is one normalized unit of CLI output.
type Event struct {
	Kind Kind
	// Text is set for assistant.text and user.message.
	Text string
	// Tool is set for tool.use and tool.result.
	Tool *Tool
	// Meta holds scalar metadata (for example token counts on turn.completed, or the failure
	// status on turn.failed). Values are plain strings.
	Meta map[string]string
	// SessionID is the CLI's own conversation/session ID when the line carried one. It never
	// leaves the server.
	SessionID string
}

// Decoder turns one complete line of CLI output into events.
//
// Lines the decoder does not understand (unknown event types, empty lines) yield no events and
// no error. A line that is not valid for the CLI's format at all returns an error, which the
// caller should count and skip rather than forward.
type Decoder interface {
	Decode(line []byte) ([]Event, error)
}

// Truncate returns s cut to at most max bytes without splitting a UTF-8 sequence, with "…"
// appended when it was cut. A max below 1 means DefaultMaxSummary.
func Truncate(s string, max int) string {
	if max < 1 {
		max = DefaultMaxSummary
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
