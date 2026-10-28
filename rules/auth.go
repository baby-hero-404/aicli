package rules

import "regexp"

// authInvalidRules is keyed by profile ref ("*" fallback for
// "custom"/unknown refs), mirroring quotaRules (quota.go) but for a
// different failure class: the linked credential is present and marked
// active, yet the CLI itself reports it isn't authenticated. Unlike quota
// (transient, cools down and retries later), this is permanent until a
// human re-runs the CLI auth capture flow — retrying burns attempts for
// nothing since the same credential produces the same failure every time.
var authInvalidRules = map[string][]*regexp.Regexp{
	"claude_code": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)please run\s*/login`),
		regexp.MustCompile(`(?i)invalid api key`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
		// "claude auth status" (the profile's AuthCheckCommand) exits 0
		// regardless of login state and reports it via this JSON field
		// instead — verified against the real `claude` binary.
		regexp.MustCompile(`(?i)"loggedIn"\s*:\s*false`),
	},
	"claude_code_notools": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)please run\s*/login`),
		regexp.MustCompile(`(?i)invalid api key`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
		regexp.MustCompile(`(?i)"loggedIn"\s*:\s*false`),
	},
	"openai_codex": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)please run\s*['"]?codex login`),
		regexp.MustCompile(`(?i)authentication required`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
	},
	"antigravity": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)not authenticated`),
		regexp.MustCompile(`(?i)authentication required`),
		regexp.MustCompile(`(?i)please (log|sign) in`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		// Printed by `agy -p` when no account is signed in (verified against agy 1.4.2).
		regexp.MustCompile(`(?i)authentication (failed|interrupted)`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
	},
	"antigravity_notools": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)not authenticated`),
		regexp.MustCompile(`(?i)authentication required`),
		regexp.MustCompile(`(?i)please (log|sign) in`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		regexp.MustCompile(`(?i)authentication (failed|interrupted)`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
	},
	"*": {
		regexp.MustCompile(`(?i)not logged in`),
		regexp.MustCompile(`(?i)not authenticated`),
		regexp.MustCompile(`(?i)authentication required`),
		regexp.MustCompile(`(?i)failed to authenticate`),
		regexp.MustCompile(`(?i)unauthorized`),
		regexp.MustCompile(`(?i)(oauth )?(access )?token (has been |was )?revoked`),
		regexp.MustCompile(`\b401\b`),
	},
}

// authInvalidTailWindowBytes bounds how much of the *end* of a CLI's output
// DetectAuthInvalid inspects, instead of the full transcript. Agentic CLIs
// routinely emit thousands of lines of tool-call trace per run and
// legitimate work on auth-related code produces incidental text matching
// these patterns mid-transcript — a test asserting a 401 response, a
// comment about a revoked token, generated auth middleware. A real "not
// authenticated" failure, by contrast, is always at or near the end of the
// output: it's the reason the CLI stopped, so there's little left to print
// afterward. Bounding the match window to the tail keeps recall for genuine
// failures (e.g. an "OAuth token revoked" message right before the process exited) while dropping mid-transcript false
// positives from unrelated, successful work.
const authInvalidTailWindowBytes = 2000

// tailWindow returns the last n bytes of s, or s unchanged if it's shorter.
func tailWindow(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// AuthInvalidConfidence classifies how strongly an auth-invalid match
// implies the credential is genuinely broken, so callers can scale their
// reaction accordingly instead of treating every match the same:
//
//   - AuthInvalidConfirmed: matched a profile-specific rule, wording
//     verified against a real binary for that exact CLI. Safe to treat as
//     permanent — skip remaining retries, disable the credential.
//   - AuthInvalidSuspected: matched only the generic "*" fallback list.
//     Broader, less specific patterns (e.g. bare "401", "unauthorized")
//     that can incidentally match legitimate output (a test asserting a 401
//     response, a comment mentioning a revoked token). Treated as an
//     ordinary retriable failure instead — worth a warning so a human can
//     add a profile-specific rule if it recurs, but not worth disabling a
//     possibly-still-good credential on a guess.
type AuthInvalidConfidence int

const (
	AuthInvalidNone AuthInvalidConfidence = iota
	AuthInvalidSuspected
	AuthInvalidConfirmed
)

// DetectAuthInvalid reports how confidently the tail of combined
// stdout/stderr for the given profile ref matches a known "not
// authenticated" signature (see authInvalidTailWindowBytes for why only the
// tail is checked). Checks the profile-specific list AND the "*" generic
// list unconditionally — a known ref (e.g. "claude_code") having its own
// entry must never shadow the generic 401/"failed to authenticate"/
// revoked-token patterns just because that specific CLI's wording wasn't
// anticipated (this exact gap is why a real "OAuth access token has been
// revoked" 401 went undetected in production: claude_code's list existed
// but didn't cover it, and the old code only fell back to "*" when the ref
// was entirely unknown).
func DetectAuthInvalid(ref string, combined string) AuthInvalidConfidence {
	window := tailWindow(combined, authInvalidTailWindowBytes)
	if ref != "*" {
		for _, p := range authInvalidRules[ref] {
			if p.MatchString(window) {
				return AuthInvalidConfirmed
			}
		}
	}
	for _, p := range authInvalidRules["*"] {
		if p.MatchString(window) {
			return AuthInvalidSuspected
		}
	}
	return AuthInvalidNone
}
