// Package rules classifies AI CLI output: quota exhaustion, invalid authentication, and
// processes blocked waiting for input. Rules are keyed by profile ref with a "*" fallback.
package rules

import (
	"regexp"
	"strconv"
	"time"
)

// quotaRule is one pattern that, if matched against a CLI invocation's
// combined stdout+stderr (or exit code), means the underlying credential hit
// a rate limit / quota ceiling — not a code or environment bug.
type quotaRule struct {
	ExitCodes []int            // matches if result.ExitCode is one of these (empty = ignored)
	Patterns  []*regexp.Regexp // matches if any pattern is found in the combined output
}

// quotaRules is keyed by profile ref (or "*" for a fallback rule set
// applied when the ref has no dedicated entry, e.g. "custom"). Config-driven
// table instead of scattered string checks — adding a CLI tool means adding
// one map entry, not touching callers' control flow. Each CLI's
// quota-exceeded message differs, so rules are not shared by default.
var quotaRules = map[string][]quotaRule{
	"claude_code": {
		{Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)usage limit reached`),
			regexp.MustCompile(`(?i)session limit`),
			regexp.MustCompile(`(?i)rate limit`),
		}},
	},
	"openai_codex": {
		{Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)rate limit`),
			regexp.MustCompile(`(?i)quota exceeded`),
			regexp.MustCompile(`(?i)too many requests`),
			regexp.MustCompile(`\b429\b`),
		}},
	},
	"antigravity": {
		{Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)quota (?:exceeded|reached)`),
			regexp.MustCompile(`(?i)rate limit`),
			regexp.MustCompile(`(?i)resource_exhausted`),
			regexp.MustCompile(`(?i)too many requests`),
			regexp.MustCompile(`\b429\b`),
		}},
	},
	"*": { // fallback for "custom" and any ref without a dedicated rule set
		{Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)rate.?limit`),
			regexp.MustCompile(`(?i)quota`),
			regexp.MustCompile(`(?i)try again later`),
		}},
	},
}

// DetectQuotaExceeded reports whether the combined output/exit code for the
// given profile ref matches a known quota-exceeded signature. Never panics
// on an unknown/empty ref — falls back to the "*" rule set.
// It also attempts to dynamically parse the cooldown duration from the error message.
func DetectQuotaExceeded(ref string, combined string, exitCode int) (bool, time.Duration) {
	rules, ok := quotaRules[ref]
	if !ok {
		rules = quotaRules["*"]
	}

	matchFound := false
	for _, rule := range rules {
		for _, code := range rule.ExitCodes {
			if code == exitCode {
				matchFound = true
				break
			}
		}
		if matchFound {
			break
		}
		for _, p := range rule.Patterns {
			if p.MatchString(combined) {
				matchFound = true
				break
			}
		}
		if matchFound {
			break
		}
	}

	if !matchFound {
		return false, 0
	}

	var cooldown time.Duration

	// Attempt to parse dynamic cooldown duration based on provider
	if ref == "antigravity" {
		// Example: "Resets in 17h40m33s"
		if m := antigravityResetRe.FindStringSubmatch(combined); len(m) > 3 {
			cooldown = parseResetDuration(m[1], m[2], m[3])
		}
	} else if ref == "claude_code" {
		// Example: "Resets 12:10pm (UTC)"
		if m := claudeResetRe.FindStringSubmatch(combined); len(m) > 1 {
			timeStr := m[1]
			// Very basic parsing attempt. Time formats vary, so we do best effort.
			// Try "3:04pm (MST)"
			t, err := time.Parse("3:04pm (MST)", timeStr)
			if err == nil {
				now := time.Now().UTC()
				target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
				if target.Before(now) {
					target = target.Add(24 * time.Hour)
				}
				cooldown = target.Sub(now)
			}
		}
	}

	return true, cooldown
}

var (
	antigravityResetRe = regexp.MustCompile(`(?i)resets\s+in\s+(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?`)
	claudeResetRe      = regexp.MustCompile(`(?i)resets\s+(\d{1,2}:\d{2}(?:am|pm)?\s*(?:\([A-Z]+\))?)`)
)

// parseResetDuration sums hour/minute/second digit strings (any may be empty). It returns 0
// when a component does not parse (e.g. overflows), so a garbled reset time degrades to
// "no cooldown hint" rather than a wrong one.
func parseResetDuration(h, m, sec string) time.Duration {
	var total time.Duration
	for _, part := range []struct {
		digits string
		unit   time.Duration
	}{{h, time.Hour}, {m, time.Minute}, {sec, time.Second}} {
		if part.digits == "" {
			continue
		}
		n, err := strconv.ParseInt(part.digits, 10, 32)
		if err != nil {
			return 0
		}
		total += time.Duration(n) * part.unit
	}
	return total
}
