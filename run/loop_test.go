package run

import "testing"

func TestLoopDetector_TriggersOnRepeatedError(t *testing.T) {
	d := newLoopDetector()
	triggered := false
	for i := 0; i < loopKillThreshold; i++ {
		if d.Push("Error: connection refused") {
			triggered = true
		}
	}
	if !triggered {
		t.Fatalf("expected loop detector to trigger after %d repeats", loopKillThreshold)
	}
}

func TestLoopDetector_IgnoresNonErrorLines(t *testing.T) {
	d := newLoopDetector()
	for i := 0; i < loopKillThreshold*2; i++ {
		if d.Push("building module foo...") {
			t.Fatalf("progress line should never trigger the loop detector")
		}
	}
}

func TestLoopDetector_DoesNotTriggerBelowThreshold(t *testing.T) {
	d := newLoopDetector()
	for i := 0; i < loopKillThreshold-1; i++ {
		if d.Push("Error: timeout") {
			t.Fatalf("should not trigger before reaching threshold")
		}
	}
}

func TestLoopDetector_WindowEvictsOldEntries(t *testing.T) {
	d := newLoopDetector()
	// Fill the window with a single repeated error just under the kill
	// threshold, then push enough distinct errors to evict them all from
	// the ring buffer; the original error's frequency should drop back out
	// of memory and not be able to trigger via stale counts.
	for i := 0; i < loopKillThreshold-1; i++ {
		d.Push("Error: A")
	}
	for i := 0; i < loopWindowSize; i++ {
		d.Push("Error: B-unique-filler")
	}
	if d.freq[hashLine("Error: A")] != 0 {
		t.Fatalf("expected evicted error to be fully removed from frequency map, got count %d", d.freq[hashLine("Error: A")])
	}
}

func TestLoopDetector_DifferentLinesDontAccumulateTogether(t *testing.T) {
	d := newLoopDetector()
	for i := 0; i < loopKillThreshold-1; i++ {
		if d.Push("Error: A") {
			t.Fatalf("should not trigger yet")
		}
		if d.Push("Error: B") {
			t.Fatalf("should not trigger yet")
		}
	}
}
