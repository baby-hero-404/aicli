package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/baby-hero-404/aicli/run/stream"
)

// Export formats.
const (
	FormatMarkdown = "md"
	FormatJSON     = "json"
)

// Exported is a rendered conversation.
type Exported struct {
	Data        []byte
	ContentType string
	// Filename is a safe suggestion for Content-Disposition.
	Filename string
}

type exportDoc struct {
	Conversation exportConversation `json:"conversation"`
	Events       []exportEvent      `json:"events"`
}

type exportConversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	Profile   string    `json:"profile"`
	Resumable bool      `json:"resumable"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type exportEvent struct {
	Seq    int64             `json:"seq"`
	TurnID string            `json:"turnId"`
	Time   time.Time         `json:"time"`
	Kind   stream.Kind       `json:"kind"`
	Text   string            `json:"text,omitempty"`
	Tool   *exportTool       `json:"tool,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

type exportTool struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	IsError bool   `json:"isError,omitempty"`
}

// Export renders the conversation (or one turn, when turnID is not empty) from its stored,
// already redacted events. It works whether or not the conversation can still be resumed.
// Neither the owner nor the CLI session ID appear in the output.
func (s *Service) Export(ctx context.Context, p Principal, id, format, turnID string) (Exported, error) {
	if format != FormatMarkdown && format != FormatJSON {
		return Exported{}, fmt.Errorf("%w: format must be %q or %q", ErrInvalidArgument, FormatMarkdown, FormatJSON)
	}
	c, err := s.load(ctx, p, id, ActionExport)
	if err != nil {
		return Exported{}, err
	}
	var events []Event
	for after := int64(0); ; {
		page, err := s.cfg.Store.ListEvents(ctx, id, after, catchUpPage)
		if err != nil {
			return Exported{}, fmt.Errorf("conversation: export: %w", err)
		}
		for _, e := range page {
			if turnID == "" || e.TurnID == turnID {
				events = append(events, e)
			}
			after = e.Seq
		}
		if len(events) > s.cfg.MaxExportEvents {
			return Exported{}, fmt.Errorf("%w: conversation is too large to export (more than %d events)", ErrInvalidArgument, s.cfg.MaxExportEvents)
		}
		if len(page) < catchUpPage {
			break
		}
	}
	s.audit(p, ActionExport, id, turnID, "ok")

	if format == FormatJSON {
		doc := exportDoc{Conversation: exportConversation{
			ID: c.ID, Title: c.Title, Profile: c.Profile, Resumable: c.Resumable, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		}, Events: make([]exportEvent, 0, len(events))}
		for _, e := range events {
			ee := exportEvent{Seq: e.Seq, TurnID: e.TurnID, Time: e.Time, Kind: e.Kind, Text: e.Text, Meta: e.Meta}
			if e.Tool != nil {
				ee.Tool = &exportTool{Name: e.Tool.Name, Summary: e.Tool.Summary, IsError: e.Tool.IsError}
			}
			doc.Events = append(doc.Events, ee)
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return Exported{}, fmt.Errorf("conversation: export json: %w", err)
		}
		return Exported{Data: b, ContentType: "application/json", Filename: "conversation-" + c.ID + ".json"}, nil
	}
	return Exported{
		Data: []byte(markdown(c, events)), ContentType: "text/markdown; charset=utf-8",
		Filename: "conversation-" + c.ID + ".md",
	}, nil
}

func markdown(c Conversation, events []Event) string {
	var b strings.Builder
	title := c.Title
	if title == "" {
		title = "Conversation"
	}
	fmt.Fprintf(&b, "# %s\n\n_Profile: %s_\n\n", oneLine(title), c.Profile)
	inAssistant := false
	for _, e := range events {
		switch e.Kind {
		case stream.KindUserMessage:
			fmt.Fprintf(&b, "## You\n\n%s\n\n", e.Text)
			inAssistant = false
		case stream.KindAssistantText:
			if !inAssistant {
				b.WriteString("## Assistant\n\n")
				inAssistant = true
			}
			b.WriteString(e.Text)
		case stream.KindToolUse:
			endAssistantLine(&b, &inAssistant)
			fmt.Fprintf(&b, "> tool `%s`: %s\n\n", e.Tool.Name, oneLine(e.Tool.Summary))
		case stream.KindToolResult:
			endAssistantLine(&b, &inAssistant)
			fmt.Fprintf(&b, "> tool `%s` finished: %s\n\n", e.Tool.Name, oneLine(e.Tool.Summary))
		case stream.KindTurnFailed:
			endAssistantLine(&b, &inAssistant)
			fmt.Fprintf(&b, "**Turn failed:** %s\n\n", e.Meta["failure"])
		case stream.KindTurnCancelled:
			endAssistantLine(&b, &inAssistant)
			b.WriteString("**Turn cancelled**\n\n")
		case stream.KindTurnCompleted:
			endAssistantLine(&b, &inAssistant)
		}
	}
	return b.String()
}

// endAssistantLine closes the assistant's running text with a blank line.
func endAssistantLine(b *strings.Builder, inAssistant *bool) {
	if *inAssistant {
		b.WriteString("\n\n")
		*inAssistant = false
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
