package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/run/stream"
	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/sandboxmock"
)

const promptInstruction = "Read and follow the complete task instructions in the file {prompt_file}, then carry them out."

func agyReq(t *testing.T) Request {
	t.Helper()
	return Request{
		Profile: "antigravity", Command: "agy",
		Args:   []string{"--dangerously-skip-permissions", "-p", promptInstruction, "--output-format", "json"},
		Prompt: "do it", Workspace: t.TempDir(),
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("stream", "agy", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func textLine(id, delta string) string {
	return fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":%q,"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":%q}}`, id, delta)
}

func collect(req *Request) *[]stream.Event {
	var got []stream.Event
	req.OnEvent = func(e stream.Event) { got = append(got, e) }
	return &got
}

func joined(evs []stream.Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == stream.KindAssistantText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func TestRun_StreamsEventsFromFixture(t *testing.T) {
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: fixture(t, "agy-1.4.8-tools.jsonl")}}}
	req := agyReq(t)
	got := collect(&req)

	res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var kinds []stream.Kind
	for _, e := range *got {
		kinds = append(kinds, e.Kind)
		if e.SessionID != "" {
			t.Errorf("event %q leaks the session ID %q", e.Kind, e.SessionID)
		}
	}
	want := []stream.Kind{stream.KindTurnStarted, stream.KindToolUse, stream.KindToolResult, stream.KindAssistantText, stream.KindTurnCompleted}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
	if res.SessionID != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("Result.SessionID = %q", res.SessionID)
	}
	if !strings.Contains(joined(*got), "secret-marker") {
		t.Errorf("assistant text = %q", joined(*got))
	}
	if res.SkippedLines != 0 {
		t.Errorf("SkippedLines = %d", res.SkippedLines)
	}
}

func TestRun_StreamingSwitchesOutputFormatAndKeepsPromptAfterDashP(t *testing.T) {
	rt := &sandboxmock.Runtime{}
	req := agyReq(t)
	req.ResumeSessionID = "conv-1"
	collect(&req)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := rt.Requests()
	if len(reqs) != 1 || reqs[0].Output == nil {
		t.Fatalf("expected one sandbox request with Output set, got %+v", reqs)
	}
	script := reqs[0].Command[2]
	for _, frag := range []string{"'--output-format' 'stream-json'", "'--conversation' 'conv-1'"} {
		if !strings.Contains(script, frag) {
			t.Errorf("script lacks %s:\n%s", frag, script)
		}
	}
	if strings.Contains(script, "'json'") {
		t.Errorf("script still asks for json output:\n%s", script)
	}
	if !strings.Contains(script, "'-p' 'Read and follow") {
		t.Errorf("-p must be directly followed by the instruction:\n%s", script)
	}
	if strings.Index(script, "'--conversation'") < strings.Index(script, "'--output-format'") {
		t.Errorf("resume args must come after the output format:\n%s", script)
	}
}

func TestRun_NoOnEventKeepsBehavior(t *testing.T) {
	rt := &sandboxmock.Runtime{}
	req := agyReq(t)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := rt.Requests()[0]
	if r.Output != nil {
		t.Error("Output must stay nil without OnEvent")
	}
	if script := r.Command[2]; !strings.Contains(script, "'--output-format' 'json'") || strings.Contains(script, "stream-json") {
		t.Errorf("non-streaming args changed:\n%s", script)
	}
}

func TestRun_OnEventRejectedForProfilesThatCannotStream(t *testing.T) {
	for _, profileRef := range []string{"claude_code", "openai_codex", "no_such_profile", ""} {
		t.Run(profileRef, func(t *testing.T) {
			rt := &sandboxmock.Runtime{}
			req := baseReq(t)
			req.Profile = profileRef
			collect(&req)
			_, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
			if n := len(rt.Requests()); n != 0 {
				t.Errorf("runtime was called %d times", n)
			}
		})
	}
}

func TestRun_RedactsSecretsSplitAcrossDeltas(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a1B2", 9) // 36 chars after the prefix
	cut := len(secret) / 2
	stdout := strings.Join([]string{
		textLine("c", "my token is "+secret[:cut]),
		textLine("c", secret[cut:]+" end\nnext line\n"),
	}, "\n") + "\n"

	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: stdout}}}
	req := agyReq(t)
	got := collect(&req)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := joined(*got)
	if strings.Contains(text, secret) || strings.Contains(text, secret[:cut]) || strings.Contains(text, secret[cut:]) {
		t.Errorf("secret leaked: %q", text)
	}
	if text != "my token is [REDACTED] end\nnext line\n" {
		t.Errorf("text = %q", text)
	}
}

func TestRun_RedactsToolSummaries(t *testing.T) {
	secret := "ghp_" + strings.Repeat("z9Y8", 9)
	line := fmt.Sprintf(`{"event":"step_update","step_update":{"conversation_id":"c","step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"cmd":"echo %s"}}}}`, secret)
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: line + "\n"}}}
	req := agyReq(t)
	got := collect(&req)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*got) != 1 || strings.Contains((*got)[0].Tool.Summary, secret) || !strings.Contains((*got)[0].Tool.Summary, "[REDACTED]") {
		t.Errorf("events = %+v", *got)
	}
}

func TestRun_FlushesUnfinishedTextAndCountsSkippedLines(t *testing.T) {
	stdout := "not json at all\n" + textLine("c", "no newline at the end")
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: stdout}}}
	req := agyReq(t)
	got := collect(&req)
	res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if joined(*got) != "no newline at the end" {
		t.Errorf("text = %q", joined(*got))
	}
	if res.SkippedLines != 1 {
		t.Errorf("SkippedLines = %d, want 1", res.SkippedLines)
	}
}

func TestRun_LongLineWithoutBreakIsFlushedInPieces(t *testing.T) {
	long := strings.Repeat("é", textHoldMax) // 2 bytes each, so it crosses the hold limit
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: textLine("c", long) + "\n"}}}
	req := agyReq(t)
	got := collect(&req)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if joined(*got) != long {
		t.Errorf("text was altered (len %d, want %d)", len(joined(*got)), len(long))
	}
	for _, e := range *got {
		if e.Kind == stream.KindAssistantText && !strings.HasSuffix(e.Text, "é") && !strings.HasSuffix(e.Text, "\n") {
			t.Error("a chunk ends in the middle of a UTF-8 sequence")
		}
	}
	if len(*got) < 2 {
		t.Errorf("expected the text in several chunks, got %d", len(*got))
	}
}

func TestRun_RuntimeErrorStillFlushesAndFails(t *testing.T) {
	rt := &sandboxmock.Runtime{Err: errors.New("boom")}
	req := agyReq(t)
	collect(&req)
	if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func initLine(id string) string {
	return fmt.Sprintf(`{"event":"init","conversation_id":%q,"init":{}}`, id)
}

func TestRun_SessionMismatchStopsAndSuppressesEvents(t *testing.T) {
	stdout := strings.Join([]string{
		initLine("brand-new"),
		textLine("brand-new", "Hello from a conversation you did not ask for"),
	}, "\n") + "\n"
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: stdout}}}
	req := agyReq(t)
	req.ResumeSessionID = "old-conversation"
	got := collect(&req)

	res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.SessionMismatch || res.Success || res.SessionID != "brand-new" {
		t.Errorf("result = %+v", res)
	}
	if len(*got) != 0 {
		t.Errorf("events leaked from the wrong session: %+v", *got)
	}
}

func TestRun_SessionMatchIsNotAMismatch(t *testing.T) {
	stdout := initLine("same") + "\n" + textLine("same", "ok\n") + "\n"
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: stdout}}}
	req := agyReq(t)
	req.ResumeSessionID = "same"
	got := collect(&req)
	res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
	if err != nil || res.SessionMismatch || res.SessionID != "same" || joined(*got) != "ok\n" {
		t.Errorf("res = %+v, err = %v, text = %q", res, err, joined(*got))
	}
}

func TestRun_FreshRunNeverMismatches(t *testing.T) {
	rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: initLine("whatever") + "\n"}}}
	req := agyReq(t)
	collect(&req)
	if res, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil || res.SessionMismatch {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestRun_MismatchCancelsTheRuntimeContext(t *testing.T) {
	rt := &ctxProbe{stdout: initLine("other") + "\n"}
	req := agyReq(t)
	req.ResumeSessionID = "old"
	collect(&req)
	res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
	if err != nil || !res.SessionMismatch {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !rt.cancelled {
		t.Error("the sandbox context was not cancelled after a mismatch")
	}
}

// ctxProbe writes stdout, then reports whether its context had been cancelled by then.
type ctxProbe struct {
	sandbox.StubRuntime
	stdout    string
	cancelled bool
}

func (c *ctxProbe) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	_, _ = io.WriteString(req.Output, c.stdout)
	c.cancelled = ctx.Err() != nil
	return nil, ctx.Err()
}

func resultLine(id, status string) string {
	return fmt.Sprintf(`{"event":"result","result":{"conversation_id":%q,"status":%q}}`, id, status)
}

// A reply that talks about rate limits or 401s is not a quota or auth failure when the CLI says
// the turn succeeded and exited cleanly.
func TestRun_ReplyMentioningFailureWordsIsStillASuccess(t *testing.T) {
	for _, reply := range []string{
		"Back off and retry when you get HTTP 429 or a rate limit error.",
		"Return 401 Unauthorized when the token is missing; the user is not authenticated.",
		"The quota exceeded path should log and continue.",
	} {
		stdout := strings.Join([]string{initLine("s1"), textLine("s1", reply), resultLine("s1", "SUCCESS")}, "\n") + "\n"
		rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: stdout}}}
		req := agyReq(t)
		collect(&req)

		res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.Success || res.QuotaExceeded || res.AuthInvalid || res.Error != "" {
			t.Errorf("reply %q misclassified: %+v", reply, res)
		}
	}
}

// The same words still count when the run did not succeed.
func TestRun_FailureSignaturesStillDetectedWhenTheRunFailed(t *testing.T) {
	tests := []struct {
		name   string
		result sandbox.CommandResult
		check  func(*Result) bool
	}{
		{"quota, non-zero exit", sandbox.CommandResult{ExitCode: 1, Stderr: "quota exceeded"}, func(r *Result) bool { return r.QuotaExceeded && !r.Success }},
		{"auth, non-zero exit", sandbox.CommandResult{ExitCode: 1, Stderr: "Not logged in"}, func(r *Result) bool { return r.AuthInvalidConfirmed && !r.Success }},
		{"auth, exit 0 but no completed turn", sandbox.CommandResult{Stdout: "Authentication failed"}, func(r *Result) bool { return r.AuthInvalid && !r.Success }},
		{
			"quota, CLI reported a failed turn",
			sandbox.CommandResult{Stdout: strings.Join([]string{initLine("s1"), resultLine("s1", "ERROR"), "RESOURCE_EXHAUSTED"}, "\n")},
			func(r *Result) bool { return r.QuotaExceeded },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{&tt.result}}
			req := agyReq(t)
			collect(&req)
			res, err := (&Runner{Runtime: rt}).Run(context.Background(), req)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !tt.check(res) {
				t.Errorf("result = %+v", res)
			}
		})
	}
}
