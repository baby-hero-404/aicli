package agy

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/run/stream"
)

const (
	idBasic    = "11111111-1111-4111-8111-111111111111"
	idTools    = "22222222-2222-4222-8222-222222222222"
	idNotFound = "33333333-3333-4333-8333-333333333333"
)

func decodeFixture(t *testing.T, name string) []stream.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var d Decoder
	var all []stream.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		evs, err := d.Decode(sc.Bytes())
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		all = append(all, evs...)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return all
}

func joinText(evs []stream.Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == stream.KindAssistantText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func kinds(evs []stream.Event) []stream.Kind {
	out := make([]stream.Kind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func TestFixtures(t *testing.T) {
	tests := []struct {
		file     string
		wantText string
		wantID   string
		wantTool bool
	}{
		{"agy-1.4.8-basic.jsonl", "pong\n", idBasic, false},
		{"agy-1.4.8-resume.jsonl", "pong\n", idBasic, false},
		{"agy-1.4.8-tools.jsonl", "", idTools, true},
		{"agy-1.4.8-notfound.jsonl", "Hello! How can I help you today?\n", idNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			evs := decodeFixture(t, tt.file)
			if len(evs) == 0 {
				t.Fatal("no events decoded")
			}
			if first := evs[0]; first.Kind != stream.KindTurnStarted && tt.file != "agy-1.4.8-resume.jsonl" && tt.file != "agy-1.4.8-tools.jsonl" {
				t.Errorf("first event = %q, want %q", first.Kind, stream.KindTurnStarted)
			}
			last := evs[len(evs)-1]
			if last.Kind != stream.KindTurnCompleted {
				t.Fatalf("last event = %q, want %q", last.Kind, stream.KindTurnCompleted)
			}
			if last.SessionID != tt.wantID {
				t.Errorf("result session ID = %q, want %q", last.SessionID, tt.wantID)
			}
			if last.Meta["total_tokens"] == "" {
				t.Errorf("turn.completed lacks usage: %v", last.Meta)
			}
			if tt.wantText != "" {
				if got := joinText(evs); got != tt.wantText {
					t.Errorf("assistant text = %q, want %q", got, tt.wantText)
				}
			}
			var sawUse, sawResult bool
			for _, e := range evs {
				switch e.Kind {
				case stream.KindToolUse:
					sawUse = true
				case stream.KindToolResult:
					sawResult = true
				}
			}
			if sawUse != tt.wantTool || sawResult != tt.wantTool {
				t.Errorf("tool events use=%v result=%v, want both %v", sawUse, sawResult, tt.wantTool)
			}
		})
	}
}

func TestToolSummaries(t *testing.T) {
	evs := decodeFixture(t, "agy-1.4.8-tools.jsonl")
	var use, res *stream.Event
	for i := range evs {
		switch evs[i].Kind {
		case stream.KindToolUse:
			use = &evs[i]
		case stream.KindToolResult:
			res = &evs[i]
		}
	}
	if use == nil || res == nil {
		t.Fatal("missing tool events")
	}
	if use.Tool.Name != "read_file" || !strings.Contains(use.Tool.Summary, `"Path":"/workspace/note.txt"`) {
		t.Errorf("tool.use = %+v", use.Tool)
	}
	if res.Tool.Name != "read_file" || res.Tool.Summary != "1 lines, 14 bytes" || res.Tool.IsError {
		t.Errorf("tool.result = %+v", res.Tool)
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		maxSum    int
		wantKinds []stream.Kind
		check     func(t *testing.T, evs []stream.Event)
	}{
		{
			name:      "init starts the turn and carries the ID",
			in:        `{"event":"init","conversation_id":"c1","init":{"cwd":"/w"}}`,
			wantKinds: []stream.Kind{stream.KindTurnStarted},
			check: func(t *testing.T, evs []stream.Event) {
				if evs[0].SessionID != "c1" {
					t.Errorf("SessionID = %q", evs[0].SessionID)
				}
			},
		},
		{
			name:      "text delta becomes assistant text",
			in:        `{"event":"step_update","step_update":{"conversation_id":"c1","step_type":"agent_response","state":"ACTIVE","text_delta":"hi"}}`,
			wantKinds: []stream.Kind{stream.KindAssistantText},
			check: func(t *testing.T, evs []stream.Event) {
				if evs[0].Text != "hi" {
					t.Errorf("Text = %q", evs[0].Text)
				}
			},
		},
		{
			name: "agent response without text yields nothing",
			in:   `{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE","usage":{"total_tokens":3}}}`,
		},
		{
			name: "user input step is ignored",
			in:   `{"event":"step_update","step_update":{"step_type":"user_input","state":"DONE"}}`,
		},
		{
			name: "unknown step type is ignored",
			in:   `{"event":"step_update","step_update":{"step_type":"hologram","state":"DONE"}}`,
		},
		{
			name: "unknown event is ignored",
			in:   `{"event":"telepathy","x":1}`,
		},
		{
			name: "step_update without payload is ignored",
			in:   `{"event":"step_update"}`,
		},
		{
			name:      "tool summary is truncated",
			in:        `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"cmd":"` + strings.Repeat("x", 100) + `"}}}}`,
			maxSum:    20,
			wantKinds: []stream.Kind{stream.KindToolUse},
			check: func(t *testing.T, evs []stream.Event) {
				if !strings.HasSuffix(evs[0].Tool.Summary, "…") || len(evs[0].Tool.Summary) > 20+len("…") {
					t.Errorf("Summary = %q", evs[0].Tool.Summary)
				}
			},
		},
		{
			name:      "tool error is flagged",
			in:        `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command","tool_info":{"error":"boom"}}}`,
			wantKinds: []stream.Kind{stream.KindToolResult},
			check: func(t *testing.T, evs []stream.Event) {
				if !evs[0].Tool.IsError || evs[0].Tool.Summary != "boom" {
					t.Errorf("Tool = %+v", evs[0].Tool)
				}
			},
		},
		{
			name:      "non-success result fails the turn",
			in:        `{"event":"result","result":{"conversation_id":"c1","status":"ERROR"}}`,
			wantKinds: []stream.Kind{stream.KindTurnFailed},
			check: func(t *testing.T, evs []stream.Event) {
				if evs[0].Meta["status"] != "ERROR" || evs[0].SessionID != "c1" {
					t.Errorf("event = %+v", evs[0])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decoder{MaxSummary: tt.maxSum}
			evs, err := d.Decode([]byte(tt.in))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			got := kinds(evs)
			if len(got) != len(tt.wantKinds) {
				t.Fatalf("kinds = %v, want %v", got, tt.wantKinds)
			}
			for i := range got {
				if got[i] != tt.wantKinds[i] {
					t.Fatalf("kinds = %v, want %v", got, tt.wantKinds)
				}
			}
			if tt.check != nil {
				tt.check(t, evs)
			}
		})
	}
}

func TestDecodeInvalidJSON(t *testing.T) {
	var d Decoder
	for _, in := range []string{`not json`, `{"event":`, `[1,2]`} {
		if evs, err := d.Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) = %v, nil; want an error", in, evs)
		}
	}
}
