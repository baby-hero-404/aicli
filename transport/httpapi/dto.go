package httpapi

import (
	"time"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/run/stream"
)

// The DTOs are the only shapes that leave the server. They deliberately omit the owner, the
// CLI session ID, and anything about the execution environment.

type conversationDTO struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Profile      string    `json:"profile"`
	Resumable    bool      `json:"resumable"`
	ActiveTurnID string    `json:"activeTurnId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func toConversation(c conversation.Conversation) conversationDTO {
	return conversationDTO{
		ID: c.ID, Title: c.Title, Profile: c.Profile, Resumable: c.Resumable,
		ActiveTurnID: c.ActiveTurnID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type turnDTO struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func toTurn(t conversation.Turn) turnDTO {
	d := turnDTO{ID: t.ID, Status: string(t.Status), StartedAt: t.StartedAt}
	if !t.FinishedAt.IsZero() {
		f := t.FinishedAt
		d.FinishedAt = &f
	}
	return d
}

type toolDTO struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	IsError bool   `json:"isError,omitempty"`
}

type eventDTO struct {
	Seq    int64             `json:"seq"`
	TurnID string            `json:"turnId,omitempty"`
	Time   time.Time         `json:"time"`
	Kind   stream.Kind       `json:"kind"`
	Text   string            `json:"text,omitempty"`
	Tool   *toolDTO          `json:"tool,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

func toEvent(e conversation.Event) eventDTO {
	d := eventDTO{Seq: e.Seq, TurnID: e.TurnID, Time: e.Time, Kind: e.Kind, Text: e.Text, Meta: e.Meta}
	if e.Tool != nil {
		d.Tool = &toolDTO{Name: e.Tool.Name, Summary: e.Tool.Summary, IsError: e.Tool.IsError}
	}
	return d
}

// Request bodies. Decoding rejects unknown fields, so a client cannot smuggle in anything else.

type createRequest struct {
	Profile string `json:"profile"`
	Title   string `json:"title"`
}

type sendRequest struct {
	Message         string `json:"message"`
	ClientMessageID string `json:"clientMessageId"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
