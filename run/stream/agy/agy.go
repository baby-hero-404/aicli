// Package agy decodes the NDJSON that Antigravity (agy) prints with
// `--output-format stream-json` into stream events.
//
// The format is not documented by agy; it is pinned by the fixtures in testdata (recorded from
// agy 1.4.8). Each line is one of:
//
//	{"event":"init","conversation_id":…,"init":{…}}
//	{"event":"step_update","step_update":{"conversation_id":…,"step_type":…,"state":…,…}}
//	{"event":"result","result":{"conversation_id":…,"status":…,"usage":{…},…}}
//
// Lines with an unknown event or step type are ignored so an agy upgrade that adds new kinds
// does not break decoding.
package agy

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/baby-hero-404/aicli/run/stream"
)

// Decoder implements stream.Decoder for agy. The zero value is ready to use.
type Decoder struct {
	// MaxSummary caps Tool.Summary in bytes; zero means stream.DefaultMaxSummary.
	MaxSummary int
}

var _ stream.Decoder = (*Decoder)(nil)

type line struct {
	Event          string      `json:"event"`
	ConversationID string      `json:"conversation_id"`
	StepUpdate     *stepUpdate `json:"step_update"`
	Result         *result     `json:"result"`
}

type stepUpdate struct {
	ConversationID string    `json:"conversation_id"`
	State          string    `json:"state"`
	StepType       string    `json:"step_type"`
	TextDelta      string    `json:"text_delta"`
	ToolName       string    `json:"tool_name"`
	ToolInfo       *toolInfo `json:"tool_info"`
}

type toolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Output     string          `json:"output"`
	Error      string          `json:"error"`
}

type result struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	NumTurns       int    `json:"num_turns"`
	Usage          *usage `json:"usage"`
}

type usage struct {
	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	ThinkingTokens int64 `json:"thinking_tokens"`
	TotalTokens    int64 `json:"total_tokens"`
}

// Decode implements stream.Decoder.
func (d *Decoder) Decode(raw []byte) ([]stream.Event, error) {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("agy: decode line: %w", err)
	}
	switch l.Event {
	case "init":
		return []stream.Event{{Kind: stream.KindTurnStarted, SessionID: l.ConversationID}}, nil
	case "step_update":
		if l.StepUpdate == nil {
			return nil, nil
		}
		return d.step(l.StepUpdate), nil
	case "result":
		if l.Result == nil {
			return nil, nil
		}
		return []stream.Event{finished(l.Result)}, nil
	}
	return nil, nil
}

func (d *Decoder) step(s *stepUpdate) []stream.Event {
	switch s.StepType {
	case "agent_response":
		if s.TextDelta == "" {
			return nil
		}
		return []stream.Event{{Kind: stream.KindAssistantText, Text: s.TextDelta, SessionID: s.ConversationID}}
	case "tool":
		return d.tool(s)
	}
	return nil
}

func (d *Decoder) tool(s *stepUpdate) []stream.Event {
	name := s.ToolName
	if name == "" && s.ToolInfo != nil {
		name = s.ToolInfo.Name
	}
	switch s.State {
	case "ACTIVE":
		var summary string
		if s.ToolInfo != nil {
			summary = compact(s.ToolInfo.Parameters)
		}
		return []stream.Event{{
			Kind:      stream.KindToolUse,
			Tool:      &stream.Tool{Name: name, Summary: stream.Truncate(summary, d.MaxSummary)},
			SessionID: s.ConversationID,
		}}
	case "DONE":
		t := &stream.Tool{Name: name}
		if s.ToolInfo != nil {
			t.Summary = stream.Truncate(s.ToolInfo.Output, d.MaxSummary)
			t.IsError = s.ToolInfo.Error != ""
			if t.IsError && t.Summary == "" {
				t.Summary = stream.Truncate(s.ToolInfo.Error, d.MaxSummary)
			}
		}
		return []stream.Event{{Kind: stream.KindToolResult, Tool: t, SessionID: s.ConversationID}}
	}
	return nil
}

func finished(r *result) stream.Event {
	ev := stream.Event{Kind: stream.KindTurnCompleted, SessionID: r.ConversationID, Meta: map[string]string{}}
	if r.Status != "SUCCESS" {
		ev.Kind = stream.KindTurnFailed
		ev.Meta["status"] = r.Status
	}
	if r.Usage != nil {
		ev.Meta["input_tokens"] = strconv.FormatInt(r.Usage.InputTokens, 10)
		ev.Meta["output_tokens"] = strconv.FormatInt(r.Usage.OutputTokens, 10)
		ev.Meta["total_tokens"] = strconv.FormatInt(r.Usage.TotalTokens, 10)
	}
	return ev
}

// compact returns raw JSON without insignificant whitespace, or "" when raw is empty or invalid.
func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(b)
}
