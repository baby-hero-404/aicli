package watch

import (
	"github.com/baby-hero-404/aicli/sandbox"
	"io"
	"strings"
	"testing"
	"time"
)

func TestActivityWriter_IdleSinceAdvancesUntilNextWrite(t *testing.T) {
	a := NewActivity(io.Discard)
	if _, err := a.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if a.IdleSince() >= 50*time.Millisecond {
		t.Fatalf("expected near-zero idle time right after a write, got %v", a.IdleSince())
	}
	time.Sleep(20 * time.Millisecond)
	if a.IdleSince() < 20*time.Millisecond {
		t.Fatalf("expected idle time to have advanced, got %v", a.IdleSince())
	}
	if _, err := a.Write([]byte("more\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if a.IdleSince() >= 20*time.Millisecond {
		t.Fatalf("expected idle time to reset after a new write, got %v", a.IdleSince())
	}
}

func TestLineLoopDetector_TriggersOnRepeatedIdenticalLine(t *testing.T) {
	d := NewLoopDetector(io.Discard)
	for i := 0; i < LoopLineThreshold-1; i++ {
		if _, err := d.Write([]byte("panic: nil pointer dereference\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if d.Detected() {
			t.Fatalf("detector fired early after %d repeats (threshold is %d)", i+1, LoopLineThreshold)
		}
	}
	if _, err := d.Write([]byte("panic: nil pointer dereference\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !d.Detected() {
		t.Fatalf("expected detector to fire after %d identical lines", LoopLineThreshold)
	}
}

func TestLineLoopDetector_DoesNotTriggerOnVariedOutput(t *testing.T) {
	d := NewLoopDetector(io.Discard)
	for i := 0; i < 500; i++ {
		if _, err := d.Write([]byte(strings.Repeat("x", i+1) + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if d.Detected() {
		t.Fatalf("detector should not fire on non-repeating output")
	}
}

func TestLineLoopDetector_HandlesPartialWritesAcrossCalls(t *testing.T) {
	d := NewLoopDetector(io.Discard)
	line := "same error every time"
	for i := 0; i < LoopLineThreshold; i++ {
		// Split each line write across two Write calls, as a real
		// stdcopy.StdCopy consumer might chunk mid-line.
		if _, err := d.Write([]byte(line[:5])); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := d.Write([]byte(line[5:] + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if !d.Detected() {
		t.Fatalf("expected detector to reassemble split writes and still trigger")
	}
}

func TestWatchForStall_TriggersOnIdleTimeout(t *testing.T) {
	activity := NewActivity(io.Discard)
	done := make(chan struct{})
	defer close(done)

	triggered := make(chan string, 1)
	go Stall(done, activity, nil, 10*time.Millisecond, 5*time.Millisecond, func(reason string) {
		triggered <- reason
	})

	select {
	case reason := <-triggered:
		if reason != sandbox.KillReasonIdleTimeout {
			t.Fatalf("expected reason %q, got %q", sandbox.KillReasonIdleTimeout, reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stall did not trigger on idle timeout")
	}
}

func TestWatchForStall_TriggersOnLoopDetected(t *testing.T) {
	activity := NewActivity(io.Discard)
	loopDet := NewLoopDetector(io.Discard)
	for i := 0; i < LoopLineThreshold; i++ {
		_, _ = loopDet.Write([]byte("stuck in a loop\n"))
	}

	done := make(chan struct{})
	defer close(done)
	triggered := make(chan string, 1)
	// A generous idle timeout so only the loop condition can plausibly fire.
	go Stall(done, activity, loopDet, time.Hour, 5*time.Millisecond, func(reason string) {
		triggered <- reason
	})

	select {
	case reason := <-triggered:
		if reason != sandbox.KillReasonLoopDetected {
			t.Fatalf("expected reason %q, got %q", sandbox.KillReasonLoopDetected, reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stall did not trigger on loop detection")
	}
}

func TestWatchForStall_StopsWithoutTriggeringWhenDoneClosedFirst(t *testing.T) {
	activity := NewActivity(io.Discard)
	done := make(chan struct{})

	triggered := make(chan string, 1)
	go Stall(done, activity, nil, time.Hour, 5*time.Millisecond, func(reason string) {
		triggered <- reason
	})
	close(done)

	select {
	case reason := <-triggered:
		t.Fatalf("expected no trigger after done closed, got %q", reason)
	case <-time.After(50 * time.Millisecond):
	}
}
