package run

import "testing"

func TestParseTelemetry_ClaudeStyle(t *testing.T) {
	output := "Some agent chatter before...\n" +
		`{"type":"result","total_cost_usd":0.1234,"duration_ms":45210,"usage":{"input_tokens":1200,"output_tokens":340}}` +
		"\ntrailing noise"

	got, ok := ParseTelemetry(output)
	if !ok {
		t.Fatalf("expected telemetry to be found")
	}
	if got.CostUSD != 0.1234 {
		t.Errorf("cost = %v, want 0.1234", got.CostUSD)
	}
	if got.DurationMS != 45210 {
		t.Errorf("duration = %v, want 45210", got.DurationMS)
	}
	if got.TokensUsed != 1540 {
		t.Errorf("tokens = %v, want 1540", got.TokensUsed)
	}
}

func TestParseTelemetry_TotalTokensField(t *testing.T) {
	output := `{"cost_usd":2.5,"duration_api_ms":900,"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":999}}`

	got, ok := ParseTelemetry(output)
	if !ok {
		t.Fatalf("expected telemetry to be found")
	}
	if got.TokensUsed != 999 {
		t.Errorf("tokens = %v, want 999 (explicit total_tokens should win over input+output)", got.TokensUsed)
	}
	if got.DurationMS != 900 {
		t.Errorf("duration = %v, want 900", got.DurationMS)
	}
}

func TestParseTelemetry_NoJSON(t *testing.T) {
	_, ok := ParseTelemetry("plain text output, no JSON at all")
	if ok {
		t.Fatalf("expected no telemetry to be found")
	}
}

func TestParseTelemetry_UnrelatedJSON(t *testing.T) {
	// Valid JSON blocks are common in agent tool-call transcripts even when
	// they carry no telemetry fields at all — must not be misread as a
	// zero-cost/zero-duration result.
	output := `{"tool":"read_file","path":"main.go"}` + "\n" + `{"status":"ok"}`
	_, ok := ParseTelemetry(output)
	if ok {
		t.Fatalf("expected no telemetry to be found in unrelated JSON")
	}
}

func TestParseTelemetry_LastMatchWins(t *testing.T) {
	// An agent transcript may contain multiple JSON objects (tool calls,
	// intermediate status); the final structured summary the CLI itself
	// prints on exit should win.
	output := `{"total_cost_usd":0.01,"duration_ms":100}` + "\n" +
		`{"total_cost_usd":0.99,"duration_ms":5000}`

	got, ok := ParseTelemetry(output)
	if !ok {
		t.Fatalf("expected telemetry to be found")
	}
	if got.CostUSD != 0.99 || got.DurationMS != 5000 {
		t.Errorf("got %+v, want the last object's values", got)
	}
}

func TestTopLevelJSONObjects_IgnoresBracesInStrings(t *testing.T) {
	input := `{"msg":"look at this literal brace: } and {"} extra`
	objs := topLevelJSONObjects(input)
	if len(objs) != 1 {
		t.Fatalf("expected exactly one top-level object, got %d: %v", len(objs), objs)
	}
	if objs[0] != `{"msg":"look at this literal brace: } and {"}` {
		t.Errorf("unexpected object: %q", objs[0])
	}
}

func TestParseTelemetry_SessionID(t *testing.T) {
	output := `{"session_id":"abc12345","conversation_id":"def67890"}`
	got, ok := ParseTelemetry(output)
	if !ok {
		t.Fatalf("expected telemetry to be found")
	}
	if got.SessionID != "abc12345" {
		t.Errorf("session_id = %q, want abc12345", got.SessionID)
	}

	// agy uses conversation_id, which should map to SessionID if session_id is not present
	output2 := `{"conversation_id":"def67890"}`
	got2, ok2 := ParseTelemetry(output2)
	if !ok2 {
		t.Fatalf("expected telemetry to be found")
	}
	if got2.SessionID != "def67890" {
		t.Errorf("session_id (from conversation_id) = %q, want def67890", got2.SessionID)
	}
}
