package run

import (
	"hash/fnv"
	"regexp"
	"strings"
)

const (
	loopWindowSize    = 50
	loopKillThreshold = 10
)

// errorLinePatterns match log lines worth tracking for loop detection.
// Only lines that look like repeated errors/failures are pushed into the
// window — this avoids false positives from normal progress/info output.
var errorLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\berror\b`),
	regexp.MustCompile(`(?i)\bfailed?\b`),
	regexp.MustCompile(`(?i)\bexception\b`),
	regexp.MustCompile(`(?i)\btraceback\b`),
	regexp.MustCompile(`(?i)\bpanic\b`),
	regexp.MustCompile(`(?i)\bretry(?:ing)?\b`),
}

func looksLikeErrorLine(line string) bool {
	for _, re := range errorLinePatterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// loopDetector tracks the last loopWindowSize error-like lines seen in a
// ring buffer and their frequency. When any single line hash recurs
// loopKillThreshold or more times within the window, the CLI process is
// considered to be looping and should be killed.
type loopDetector struct {
	ring   [loopWindowSize]uint64
	filled [loopWindowSize]bool
	idx    int
	freq   map[uint64]int
}

func newLoopDetector() *loopDetector {
	return &loopDetector{freq: make(map[uint64]int)}
}

// Push records a raw output line and returns true once the loop-kill
// threshold has been reached. Lines that don't look like errors are ignored.
func (d *loopDetector) Push(line string) bool {
	if !looksLikeErrorLine(line) {
		return false
	}
	h := hashLine(line)

	if d.filled[d.idx] {
		evicted := d.ring[d.idx]
		d.freq[evicted]--
		if d.freq[evicted] <= 0 {
			delete(d.freq, evicted)
		}
	}
	d.ring[d.idx] = h
	d.filled[d.idx] = true
	d.idx = (d.idx + 1) % loopWindowSize

	d.freq[h]++
	return d.freq[h] >= loopKillThreshold
}

func hashLine(line string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(line))
	return h.Sum64()
}

// DetectLoop reports whether the same error-looking line recurs often enough in output to
// indicate the CLI is stuck in a retry loop. It complements the sandbox's live loop detector,
// catching loops confined to a short burst that exits before the live threshold trips.
func DetectLoop(output string) bool {
	d := newLoopDetector()
	triggered := false
	for line := range strings.SplitSeq(output, "\n") {
		if d.Push(line) {
			triggered = true
		}
	}
	return triggered
}
