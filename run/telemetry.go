package run

import "encoding/json"

// cliTelemetry is a permissive superset of the trailing JSON summary object
// claude/codex/agy's `--output-format json` (or equivalent) prints on exit.
// Every field is optional
// since not every CLI/version reports every metric; ParseTelemetry
// tolerates whichever subset is present and leaves the rest zero.
type cliTelemetry struct {
	TotalCostUSD   *float64 `json:"total_cost_usd"`
	CostUSD        *float64 `json:"cost_usd"`
	DurationMS     *int64   `json:"duration_ms"`
	DurationAPI    *int64   `json:"duration_api_ms"`
	TokensUsed     *int64   `json:"tokens_used"`
	SessionID      *string  `json:"session_id"`
	ConversationID *string  `json:"conversation_id"`
	Usage          *struct {
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		TotalTokens  *int64 `json:"total_tokens"`
	} `json:"usage"`
}

// Telemetry is the normalized usage a CLI reported on exit.
type Telemetry struct {
	CostUSD    float64
	DurationMS int64
	TokensUsed int64
	SessionID  string
}

// ParseTelemetry scans combined subprocess output for the last
// syntactically valid top-level JSON object and extracts cost/duration/token
// metrics from it, tolerating surrounding plain-text output (agentic CLIs
// print progress/tool-call lines before the final structured summary, even
// with --output-format json). Returns ok=false if no JSON object in the
// output parses into any recognized telemetry field, so callers don't record
// a spurious all-zero row for CLIs/providers that don't emit one yet (e.g.
// codex, until its structured-output schema is confirmed).
func ParseTelemetry(output string) (Telemetry, bool) {
	var best Telemetry
	found := false

	for _, blob := range topLevelJSONObjects(output) {
		var t cliTelemetry
		if err := json.Unmarshal([]byte(blob), &t); err != nil {
			continue
		}
		var candidate Telemetry
		matched := false
		switch {
		case t.TotalCostUSD != nil:
			candidate.CostUSD = *t.TotalCostUSD
			matched = true
		case t.CostUSD != nil:
			candidate.CostUSD = *t.CostUSD
			matched = true
		}
		switch {
		case t.DurationMS != nil:
			candidate.DurationMS = *t.DurationMS
			matched = true
		case t.DurationAPI != nil:
			candidate.DurationMS = *t.DurationAPI
			matched = true
		}
		switch {
		case t.TokensUsed != nil:
			candidate.TokensUsed = *t.TokensUsed
			matched = true
		case t.Usage != nil:
			if t.Usage.TotalTokens != nil {
				candidate.TokensUsed = *t.Usage.TotalTokens
			} else {
				candidate.TokensUsed = t.Usage.InputTokens + t.Usage.OutputTokens
			}
			matched = true
		}
		switch {
		case t.SessionID != nil:
			candidate.SessionID = *t.SessionID
			matched = true
		case t.ConversationID != nil:
			candidate.SessionID = *t.ConversationID
			matched = true
		}
		if matched {
			best = candidate
			found = true
		}
	}
	return best, found
}

// topLevelJSONObjects returns every substring of s that is a balanced,
// top-level `{...}` object (brace-depth tracked, ignoring braces inside
// string literals), in the order they appear. Cheaper and more robust than a
// regex against arbitrary CLI chatter — output is scanned once, byte by
// byte, with no assumption about where the JSON starts (front, middle, or
// end of the combined stream).
func topLevelJSONObjects(s string) []string {
	var objs []string
	depth := 0
	start := -1
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					objs = append(objs, s[start:i+1])
					start = -1
				}
			}
		}
	}
	return objs
}
