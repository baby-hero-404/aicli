package rules

import (
	"testing"
	"time"
)

func TestDetectQuotaExceeded(t *testing.T) {
	cases := []struct {
		name     string
		ref      string
		combined string
		exitCode int
		want     bool
		wantDur  time.Duration
	}{
		{"claude_code usage limit", "claude_code", "Error: usage limit reached, please try again later", 1, true, 0},
		{"claude_code session limit no parse", "claude_code", "You've hit your session limit · resets 7:10pm", 1, true, 0},
		{"claude_code unrelated failure", "claude_code", "SyntaxError: unexpected token", 1, false, 0},
		{"openai_codex rate limit", "openai_codex", "OpenAI API error: rate limit exceeded", 1, true, 0},
		{"openai_codex 429", "openai_codex", "request failed with status 429", 1, true, 0},
		{"openai_codex too many requests", "openai_codex", "HTTP 429 Too Many Requests", 1, true, 0},
		{"antigravity quota", "antigravity", "quota exceeded for this account", 1, true, 0},
		{"antigravity quota reached", "antigravity", `{"error":"Individual quota reached. Please upgrade your subscription"}`, 1, true, 0},
		{"antigravity quota with duration", "antigravity", `{"error":"Individual quota reached. Resets in 17h40m33s."}`, 1, true, 17*time.Hour + 40*time.Minute + 33*time.Second},
		{"antigravity resource exhausted", "antigravity", "RESOURCE_EXHAUSTED: quota exceeded", 1, true, 0},
		{"unknown ref falls back to *", "some_future_tool", "please try again later", 1, true, 0},
		{"unknown ref, no match", "some_future_tool", "panic: nil pointer dereference", 1, false, 0},
		{"empty ref (legacy fallback) matches *", "", "Rate Limit hit", 1, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotDur := DetectQuotaExceeded(tc.ref, tc.combined, tc.exitCode)
			if got != tc.want {
				t.Errorf("DetectQuotaExceeded(%q, %q, %d) = %v, want %v", tc.ref, tc.combined, tc.exitCode, got, tc.want)
			}
			// For claude_code time parsing, the duration depends on time.Now(), so we only check exact durations if wantDur > 0
			// (Or if the test case specifically checks it).
			if tc.wantDur > 0 && gotDur != tc.wantDur {
				t.Errorf("DetectQuotaExceeded(%q, %q, %d) returned duration %v, want %v", tc.ref, tc.combined, tc.exitCode, gotDur, tc.wantDur)
			}
		})
	}
}

func TestParseResetDuration(t *testing.T) {
	tests := []struct {
		name    string
		h, m, s string
		want    time.Duration
	}{
		{"full", "17", "40", "33", 17*time.Hour + 40*time.Minute + 33*time.Second},
		{"minutes only", "", "5", "", 5 * time.Minute},
		{"none", "", "", "", 0},
		{"overflow degrades to zero", "99999999999999999999", "1", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseResetDuration(tt.h, tt.m, tt.s); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
