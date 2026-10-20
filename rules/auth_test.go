package rules

import (
	"strings"
	"testing"
)

func TestDetectAuthInvalid(t *testing.T) {
	cases := []struct {
		name     string
		ref      string
		combined string
		want     AuthInvalidConfidence
	}{
		{"claude_code not logged in", "claude_code", "Not logged in · Please run /login\n", AuthInvalidConfirmed},
		// Real production message: claude_code's own list didn't cover it
		// (only "not logged in" / "please run /login" / "invalid api key"),
		// so it silently fell through undetected until the "*" list was
		// made an unconditional supplement instead of an unknown-ref fallback.
		// claude_code's own list has since grown a revoked-token pattern too,
		// so this now matches profile-specific (Confirmed).
		{"claude_code revoked oauth token", "claude_code", "Failed to authenticate. API Error: 401 OAuth access token has been revoked.\n", AuthInvalidConfirmed},
		{"claude_code auth status loggedIn false", "claude_code", `{"loggedIn": false}`, AuthInvalidConfirmed},
		{"claude_code unrelated failure", "claude_code", "SyntaxError: unexpected token", AuthInvalidNone},
		// Real agy 1.4.2 output when no account is signed in.
		{"antigravity models logged out", "antigravity", "Fetching available models...\nError: Please sign in to view available models. Launch the CLI without arguments to sign in.", AuthInvalidConfirmed},
		{"antigravity print logged out", "antigravity", "Error: authentication interrupted.\nerror: authentication failed or timed out\n" + `{"conversation_id":"","status":"ERROR","error":"authentication failed or timed out"}`, AuthInvalidConfirmed},
		{"openai_codex not logged in", "openai_codex", "Error: not logged in, please run codex login", AuthInvalidConfirmed},
		// Only the generic "*" list covers this ref/phrasing combination —
		// medium confidence, not verified against a real binary.
		{"unknown ref matches generic 401 (suspected)", "some_future_tool", "request failed: 401 unauthorized", AuthInvalidSuspected},
		{"unknown ref, no match", "some_future_tool", "panic: nil pointer dereference", AuthInvalidNone},
		{"empty ref matches generic (suspected)", "", "authentication required", AuthInvalidSuspected},
		{"all good, task complete", "claude_code", "all good, task complete", AuthInvalidNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectAuthInvalid(tc.ref, tc.combined)
			if got != tc.want {
				t.Errorf("DetectAuthInvalid(%q, %q) = %v, want %v", tc.ref, tc.combined, got, tc.want)
			}
		})
	}
}

// TestDetectAuthInvalid_TailWindow guards the fix for the false-positive
// risk in the "*" fallback: a long agentic trace that mentions an
// auth-invalid phrase early on (as part of legitimate work — writing a test
// for a 401 response, discussing a revoked-token flow) but goes on to finish
// normally must not be flagged, while a genuine failure whose message is
// near the end of a long transcript must still be caught.
func TestDetectAuthInvalid_TailWindow(t *testing.T) {
	filler := strings.Repeat("tool_call: read_file(\"src/auth.go\")\n", 200) // well over authInvalidTailWindowBytes

	t.Run("mid-transcript mention in unrelated successful work is not flagged", func(t *testing.T) {
		combined := "Failed to authenticate is the string this task's test asserts on: 401 unauthorized.\n" + filler + "Done. All tests pass.\n"
		if got := DetectAuthInvalid("claude_code", combined); got != AuthInvalidNone {
			t.Errorf("expected no match (got %v): the auth-invalid phrase is far from the tail, the run actually finished cleanly", got)
		}
	})

	t.Run("genuine failure near the end of a long transcript is still caught", func(t *testing.T) {
		combined := filler + "Not logged in · Please run /login\n"
		if got := DetectAuthInvalid("claude_code", combined); got != AuthInvalidConfirmed {
			t.Errorf("expected AuthInvalidConfirmed (got %v): the real failure message is within the tail window", got)
		}
	})

	t.Run("short output unaffected by windowing", func(t *testing.T) {
		if got := DetectAuthInvalid("claude_code", "Not logged in · Please run /login\n"); got != AuthInvalidConfirmed {
			t.Errorf("expected AuthInvalidConfirmed (got %v) for output shorter than the tail window", got)
		}
	})
}
