package docker

import (
	"github.com/baby-hero-404/aicli/sandbox"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// loopLineThreshold/loopWindowSize define loop detection: the same line appearing 10+ times
// within a bounded recent window of the raw output stream. It tracks every non-blank line as
// it arrives mid-run, not just error-looking ones, because the goal is a fast kill switch,
// not a precise root-cause classifier.
const (
	loopLineThreshold = 10
	loopWindowSize    = 200
)

// activityWriter wraps an io.Writer and records the time of the last Write,
// so a watchdog can detect an idle (no stdout/stderr output at all)
// subprocess and kill it instead of only
// enforcing an absolute Timeout regardless of whether the process is still
// actively working.
type activityWriter struct {
	w    io.Writer
	last atomic.Int64 // unix nano
}

func newActivityWriter(w io.Writer) *activityWriter {
	a := &activityWriter{w: w}
	a.last.Store(time.Now().UnixNano())
	return a
}

func (a *activityWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// IdleSince returns how long it has been since the last Write.
func (a *activityWriter) IdleSince() time.Duration {
	return time.Since(time.Unix(0, a.last.Load()))
}

// lineLoopDetector wraps an io.Writer, splitting the incoming byte stream on
// newlines and flagging (Detected) once an identical line has recurred
// loopLineThreshold or more times within the last loopWindowSize non-blank
// lines seen — a live equivalent of engine.loopDetector, run as the
// subprocess streams output rather than after it has already finished.
type lineLoopDetector struct {
	w io.Writer

	mu       sync.Mutex
	pending  strings.Builder
	window   []string
	counts   map[string]int
	detected atomic.Bool
}

func newLineLoopDetector(w io.Writer) *lineLoopDetector {
	return &lineLoopDetector{w: w, counts: make(map[string]int)}
}

func (d *lineLoopDetector) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)

	d.mu.Lock()
	d.pending.Write(p)
	buffered := d.pending.String()
	parts := strings.Split(buffered, "\n")
	// The last element may be a partial (not-yet-newline-terminated) line;
	// keep it buffered for the next Write rather than counting it early.
	d.pending.Reset()
	d.pending.WriteString(parts[len(parts)-1])
	for _, line := range parts[:len(parts)-1] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		d.pushLocked(line)
	}
	d.mu.Unlock()

	return n, err
}

func (d *lineLoopDetector) pushLocked(line string) {
	d.window = append(d.window, line)
	d.counts[line]++
	if len(d.window) > loopWindowSize {
		evicted := d.window[0]
		d.window = d.window[1:]
		d.counts[evicted]--
		if d.counts[evicted] <= 0 {
			delete(d.counts, evicted)
		}
	}
	if d.counts[line] >= loopLineThreshold {
		d.detected.Store(true)
	}
}

// Detected reports whether the repeated-line threshold has been crossed.
func (d *lineLoopDetector) Detected() bool {
	return d.detected.Load()
}

// watchForStall polls activity/loop every checkInterval and calls onTrigger
// exactly once (with the reason: sandbox.KillReasonIdleTimeout or
// sandbox.KillReasonLoopDetected) the first time either condition is met, then
// returns. Stops without triggering when done is closed first (the normal
// "container already exited" path). Runs in its own goroutine, started by
// the caller.
func watchForStall(done <-chan struct{}, activity *activityWriter, loopDet *lineLoopDetector, idleTimeout, checkInterval time.Duration, onTrigger func(reason string)) {
	if checkInterval <= 0 {
		checkInterval = 5 * time.Second
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if loopDet != nil && loopDet.Detected() {
				onTrigger(sandbox.KillReasonLoopDetected)
				return
			}
			if activity != nil && idleTimeout > 0 && activity.IdleSince() >= idleTimeout {
				onTrigger(sandbox.KillReasonIdleTimeout)
				return
			}
		}
	}
}
