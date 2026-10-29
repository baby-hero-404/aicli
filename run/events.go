package run

import (
	"fmt"
	"strings"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/run/stream"
	"github.com/baby-hero-404/aicli/run/stream/agy"
)

const (
	// textHoldMax bounds how much assistant text is held back waiting for a line break.
	textHoldMax = 16 << 10
	// textHoldTail is what stays unsent when the hold limit forces a flush mid-line, so a
	// secret straddling the cut is still seen whole by the next redaction.
	textHoldTail = 1 << 10
)

func errNoStreaming(ref string) error {
	return fmt.Errorf("%w: profile %q cannot stream events", ErrInvalidRequest, ref)
}

// decoderFor returns the stream decoder for a profile ref, or false when there is none.
func decoderFor(ref string) (stream.Decoder, bool) {
	switch ref {
	case "antigravity", "antigravity_notools":
		return &agy.Decoder{}, true
	}
	return nil, false
}

// eventSink turns the CLI's stdout lines into redacted stream events for Request.OnEvent.
//
// Assistant text arrives in small deltas that can cut a secret in two, so text is held until a
// line break and every complete line is redacted as a whole before it is sent. Everything
// else is redacted per event. The CLI's own session ID is kept for Result and removed from the
// forwarded events.
type eventSink struct {
	decoder   stream.Decoder
	onEvent   func(stream.Event)
	text      strings.Builder
	skipped   int
	sessionID string

	// expected is the session the run was asked to resume ("" for a new one). If the CLI
	// reports a different one, mismatch is set, onMismatch is called once, and every later
	// line is dropped: agy silently starts a fresh conversation for an unknown ID, and its
	// output must not be shown as the continuation of the old one.
	expected   string
	mismatch   bool
	onMismatch func()
}

func newEventSink(ref, expected string, onEvent func(stream.Event)) (*eventSink, error) {
	if _, ok := profile.Lookup(ref); !ok {
		return nil, errNoStreaming(ref)
	}
	dec, ok := decoderFor(ref)
	if !ok {
		return nil, errNoStreaming(ref)
	}
	return &eventSink{decoder: dec, onEvent: onEvent, expected: expected}, nil
}

// line decodes one complete stdout line.
func (s *eventSink) line(b []byte) {
	if s.mismatch {
		return
	}
	evs, err := s.decoder.Decode(b)
	if err != nil {
		s.skipped++
		return
	}
	for _, ev := range evs {
		s.push(ev)
	}
}

func (s *eventSink) push(ev stream.Event) {
	if s.mismatch {
		return
	}
	if ev.SessionID != "" && s.expected != "" && ev.SessionID != s.expected {
		s.mismatch = true
		s.sessionID = ev.SessionID
		s.text.Reset()
		if s.onMismatch != nil {
			s.onMismatch()
		}
		return
	}
	if ev.SessionID != "" && (s.sessionID == "" || ev.Kind == stream.KindTurnCompleted || ev.Kind == stream.KindTurnFailed) {
		s.sessionID = ev.SessionID
	}
	ev.SessionID = ""
	if ev.Kind == stream.KindAssistantText {
		s.text.WriteString(ev.Text)
		s.sendCompleteLines()
		return
	}
	s.flushText()
	if ev.Tool != nil {
		t := *ev.Tool
		t.Summary = RedactSecrets(t.Summary)
		ev.Tool = &t
	}
	s.onEvent(ev)
}

// sendCompleteLines sends every complete line held so far and keeps the unfinished tail.
func (s *eventSink) sendCompleteLines() {
	held := s.text.String()
	if i := strings.LastIndexByte(held, '\n'); i >= 0 {
		s.sendText(held[:i+1])
		s.reset(held[i+1:])
		held = s.text.String()
	}
	if len(held) > textHoldMax {
		cut := len(held) - textHoldTail
		for cut < len(held) && held[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
			cut++
		}
		s.sendText(held[:cut])
		s.reset(held[cut:])
	}
}

// flushText sends whatever text is still held, including an unfinished last line.
func (s *eventSink) flushText() {
	if s.text.Len() == 0 {
		return
	}
	s.sendText(s.text.String())
	s.text.Reset()
}

func (s *eventSink) sendText(chunk string) {
	if chunk == "" {
		return
	}
	// Redact line by line: the patterns are meant for single lines.
	lines := strings.SplitAfter(chunk, "\n")
	for i, l := range lines {
		lines[i] = RedactSecrets(l)
	}
	s.onEvent(stream.Event{Kind: stream.KindAssistantText, Text: strings.Join(lines, "")})
}

func (s *eventSink) reset(rest string) {
	s.text.Reset()
	s.text.WriteString(rest)
}
