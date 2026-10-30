package toolchain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

func current(t *testing.T, s *Store, tool string) string {
	t.Helper()
	v, err := s.Current(tool)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Sync makes a new release current at once and remembers what was current, so it can go back.
func TestSync_NewReleaseIsUsedAtOnceAndRemembersThePreviousOne(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")

	res := syncClaude(t, in, SyncOptions{})
	if current(t, s, "claude") != "2.1.312" || res.Activated["claude"] != "2.1.312" {
		t.Fatalf("current = %q, activated = %v", current(t, s, "claude"), res.Activated)
	}
	if prev, ok, _ := s.Previous("claude"); !ok || prev != "2.1.311" {
		t.Errorf("previous = %q ok=%v, want 2.1.311", prev, ok)
	}
}

func TestStore_RollbackGoesBackAndMarksTheVersionBad(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "claude", "2.1.311")
	fakeInstall(t, s, "claude", "2.1.312")
	_ = s.Use("claude", "2.1.311")
	_ = s.Use("claude", "2.1.312")

	ev, err := s.Rollback("claude", "crashes on start")
	if err != nil || ev.From != "2.1.312" || ev.To != "2.1.311" {
		t.Fatalf("Rollback = %+v, %v", ev, err)
	}
	if current(t, s, "claude") != "2.1.311" {
		t.Errorf("current = %q", current(t, s, "claude"))
	}
	if bad, _ := s.BadVersions("claude"); !strings.Contains(bad["2.1.312"], "crashes on start") {
		t.Errorf("bad = %v", bad)
	}
	// the version just left is not the target of the next rollback
	if _, err := s.Rollback("claude", "again"); !errors.Is(err, ErrNoPrevious) {
		t.Errorf("a second rollback = %v, want ErrNoPrevious", err)
	}
}

func TestStore_RollbackToTheImageWhenNothingWasInstalledBefore(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "claude", "2.1.312")
	_ = s.Use("claude", "2.1.312") // first setup: the image's CLI was in use before

	ev, err := s.Rollback("claude", "broken")
	if err != nil || ev.To != "" || current(t, s, "claude") != "" {
		t.Fatalf("Rollback = %+v, %v, current %q; want the image's CLI again", ev, err, current(t, s, "claude"))
	}
	if _, err := newStore(t).Rollback("claude", "x"); !errors.Is(err, ErrNoPrevious) {
		t.Errorf("rollback with nothing recorded = %v", err)
	}
}

func TestStore_RollbackFailsCleanlyWhenThePreviousVersionIsGone(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "claude", "2.1.311")
	fakeInstall(t, s, "claude", "2.1.312")
	_ = s.Use("claude", "2.1.311")
	_ = s.Use("claude", "2.1.312")
	if err := s.Remove("claude", "2.1.311"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback("claude", "x"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
	if current(t, s, "claude") != "2.1.312" {
		t.Error("a failed rollback changed current")
	}
}

func TestSync_NeverPrunesThePreviousVersion(t *testing.T) {
	in, s := installer(t, claudeLatest())
	for _, v := range []string{"2.1.308", "2.1.309", "2.1.310", "2.1.311"} {
		fakeInstall(t, s, "claude", v)
	}
	_ = s.Use("claude", "2.1.311")

	// Keep 1 would leave only the newest, but the version a rollback goes back to must stay.
	syncClaude(t, in, SyncOptions{Keep: 1})
	got, _ := s.Versions("claude")
	if want := []string{"2.1.311", "2.1.312"}; !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v (the new current and the previous one)", got, want)
	}
	if _, err := s.Rollback("claude", "test"); err != nil {
		t.Errorf("the rollback target was pruned: %v", err)
	}
}

func TestSync_AReleaseThatFailsItsCheckIsMarkedBadAndNotActivated(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	fail := func(context.Context, string, string, string) error { return errors.New("smoke test failed") }

	_, err := Sync(context.Background(), in, SyncOptions{Tools: []string{"claude"}, Check: fail})
	if err == nil || !strings.Contains(err.Error(), "smoke test failed") {
		t.Fatalf("err = %v", err)
	}
	if current(t, s, "claude") != "2.1.311" {
		t.Errorf("current = %q, want the old version kept", current(t, s, "claude"))
	}
	if bad, _ := s.BadVersions("claude"); bad["2.1.312"] == "" {
		t.Errorf("bad = %v", bad)
	}

	// next cycle: the bad release is not tried again, even with a passing check...
	pass := func(context.Context, string, string, string) error { return nil }
	syncClaude(t, in, SyncOptions{Check: pass})
	if current(t, s, "claude") != "2.1.311" {
		t.Fatalf("a bad release was activated later: %q", current(t, s, "claude"))
	}
	// ...but a newer release is
	f := claudeLatest()
	f.latest, f.versionOut = "2.1.313", "2.1.313 (Claude Code)"
	in.Runtime = f
	syncClaude(t, in, SyncOptions{Check: pass})
	if current(t, s, "claude") != "2.1.313" {
		t.Errorf("a newer release after a bad one was not used: %q", current(t, s, "claude"))
	}
}

func healthCfg(now *time.Time) HealthConfig {
	return HealthConfig{Probation: time.Hour, MaxFailures: 3, Now: func() time.Time { return *now }}
}

// A release Sync switched to is rolled back after failures in a row; the old one is current again,
// the new one is marked bad, and the next Sync does not bring it back.
func TestReportOutcome_RollsBackAfterFailuresInARow(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	now := time.Now()
	syncClaude(t, in, SyncOptions{Now: func() time.Time { return now }})
	cfg := healthCfg(&now)

	for i := 1; i <= 2; i++ {
		if ev, err := s.ReportOutcome("claude", "2.1.312", false, "exit code 139", cfg); err != nil || ev != nil {
			t.Fatalf("failure %d: ev=%v err=%v, want no rollback yet", i, ev, err)
		}
	}
	// a success resets the streak
	_, _ = s.ReportOutcome("claude", "2.1.312", true, "", cfg)
	for i := 1; i <= 2; i++ {
		if ev, _ := s.ReportOutcome("claude", "2.1.312", false, "exit code 139", cfg); ev != nil {
			t.Fatalf("rolled back after %d failures following a success", i)
		}
	}
	ev, err := s.ReportOutcome("claude", "2.1.312", false, "exit code 139", cfg)
	if err != nil || ev == nil || ev.From != "2.1.312" || ev.To != "2.1.311" {
		t.Fatalf("third failure in a row: ev=%+v err=%v", ev, err)
	}
	if current(t, s, "claude") != "2.1.311" {
		t.Errorf("current = %q", current(t, s, "claude"))
	}
	syncClaude(t, in, SyncOptions{}) // the next cycle
	if current(t, s, "claude") != "2.1.311" {
		t.Errorf("Sync brought the bad release back: %q", current(t, s, "claude"))
	}
}

func TestReportOutcome_OnlyAppliesToAReleaseOnProbation(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	now := time.Now()
	syncClaude(t, in, SyncOptions{Now: func() time.Time { return now }})
	cfg := healthCfg(&now)
	// fail reports five failures and returns the rollback if ANY of them caused one (the third does,
	// so looking only at the last call would miss it).
	fail := func(version string) *RollbackEvent {
		var rolledBack *RollbackEvent
		for i := 0; i < 5; i++ {
			if ev, _ := s.ReportOutcome("claude", version, false, "exit code 139", cfg); ev != nil {
				rolledBack = ev
			}
		}
		return rolledBack
	}

	if fail("2.1.311") != nil { // not the release on probation
		t.Error("a version Sync did not switch to was rolled back")
	}
	later := now.Add(2 * time.Hour) // probation (1h) is over
	cfg2 := cfg
	cfg2.Now = func() time.Time { return later }
	for i := 0; i < 5; i++ {
		if ev, _ := s.ReportOutcome("claude", "2.1.312", false, "x", cfg2); ev != nil {
			t.Fatal("a failure after the probation rolled back")
		}
	}
	if current(t, s, "claude") != "2.1.312" {
		t.Errorf("current = %q, want it untouched", current(t, s, "claude"))
	}

	// an operator's own choice is never rolled back automatically
	_ = s.Use("claude", "2.1.311")
	_ = s.Use("claude", "2.1.312")
	if fail("2.1.312") != nil {
		t.Error("a version the operator chose was rolled back")
	}
}

// recordingRuntime is the sandbox under toolchain.Runtime: it answers with a scripted exit code.
type recordingRuntime struct {
	sandbox.StubRuntime
	exit int
}

func (r recordingRuntime) Run(context.Context, sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	return &sandbox.CommandResult{ExitCode: r.exit}, nil
}

func TestRuntime_RollsBackABrokenReleaseOnItsOwn(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	syncClaude(t, in, SyncOptions{})

	var mu sync.Mutex
	var events []RollbackEvent
	inner := &recordingRuntime{exit: 139} // segmentation fault
	rt := Wrap(inner, s)
	rt.OnRollback = func(e RollbackEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }
	run := func(command ...string) {
		if _, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: command}); err != nil {
			t.Fatal(err)
		}
	}

	run("bash", "-lc", "cd /workspace && claude -p hi") // 1
	run("bash", "-lc", "claude -p hi")                  // 2
	if current(t, s, "claude") != "2.1.312" {
		t.Fatal("rolled back too early")
	}
	run("bash", "-lc", "claude -p hi") // 3
	if current(t, s, "claude") != "2.1.311" {
		t.Fatalf("current = %q, want the old release after 3 crashes", current(t, s, "claude"))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].From != "2.1.312" || events[0].To != "2.1.311" {
		t.Errorf("OnRollback events = %+v", events)
	}
}

// Only clear signals count, and only for tools the command is about.
func TestRuntime_IgnoresUnrelatedAndAmbiguousResults(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	syncClaude(t, in, SyncOptions{})

	for name, tc := range map[string]struct {
		exit    int
		command []string
	}{
		"the task failed (exit 1)":       {1, []string{"bash", "-lc", "claude -p hi"}},
		"quota/other CLI error (exit 2)": {2, []string{"bash", "-lc", "claude -p hi"}},
		"another program crashed":        {139, []string{"bash", "-lc", "make test"}},
		"a word that only contains it":   {139, []string{"bash", "-lc", "claudette run"}},
		"a path that only contains it":   {139, []string{"bash", "-lc", "ls my-claude-notes"}},
	} {
		t.Run(name, func(t *testing.T) {
			rt := Wrap(&recordingRuntime{exit: tc.exit}, s)
			for i := 0; i < 5; i++ {
				_, _ = rt.Run(context.Background(), sandbox.CommandRequest{Command: tc.command})
			}
			if current(t, s, "claude") != "2.1.312" {
				t.Errorf("rolled back on %v exit %d", tc.command, tc.exit)
			}
		})
	}
}

func TestMentions(t *testing.T) {
	for _, tc := range []struct {
		command, program string
		want             bool
	}{
		{"claude -p hi", "claude", true},
		{"cd /w && claude -p hi", "claude", true},
		{"/opt/aicli/tools/claude/bin/claude --version", "claude", true},
		{"agency run", "agy", false},
		{"magy", "agy", false},
		{"my-agy-notes", "agy", false},
		{"agy", "agy", true},
		{"echo agy; agy models", "agy", true},
	} {
		if got := mentions(tc.command, tc.program); got != tc.want {
			t.Errorf("mentions(%q, %q) = %v, want %v", tc.command, tc.program, got, tc.want)
		}
	}
}

// failing reports n failures for a version and returns the rollback if any of them caused one.
func failing(t *testing.T, s *Store, tool, version string, n int, cfg HealthConfig) *RollbackEvent {
	t.Helper()
	var rolledBack *RollbackEvent
	for i := 0; i < n; i++ {
		ev, err := s.ReportOutcome(tool, version, false, "exit code 139", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if ev != nil {
			rolledBack = ev
		}
	}
	return rolledBack
}

// A version the operator picks on purpose is trusted, even when it is the very release Sync had
// switched to: `use` and `reset` end the probation.
func TestProbation_EndsWhenTheOperatorChoosesAVersion(t *testing.T) {
	for name, act := range map[string]func(*Store){
		"use the same release again": func(s *Store) {
			_ = s.Use("claude", "2.1.311")
			_ = s.Use("claude", "2.1.312")
		},
		"use the release directly": func(s *Store) { _ = s.Use("claude", "2.1.312") },
		"reset, then use it": func(s *Store) {
			_ = s.Unuse("claude")
			_ = s.Use("claude", "2.1.312")
		},
	} {
		t.Run(name, func(t *testing.T) {
			in, s := installer(t, claudeLatest())
			fakeInstall(t, s, "claude", "2.1.311")
			_ = s.Use("claude", "2.1.311")
			now := time.Now()
			syncClaude(t, in, SyncOptions{Now: func() time.Time { return now }}) // 2.1.312, on probation
			act(s)

			if ev := failing(t, s, "claude", "2.1.312", 6, healthCfg(&now)); ev != nil {
				t.Errorf("a version the operator chose was rolled back: %+v", ev)
			}
			if current(t, s, "claude") != "2.1.312" {
				t.Errorf("current = %q", current(t, s, "claude"))
			}
		})
	}
}

// 134 is how Node reports running out of heap, and 137 the OOM killer: heavy tasks, not a broken
// release. 139 (segfault) and 127 (not found) are.
func TestRuntime_OnlyRealCrashesCountAgainstARelease(t *testing.T) {
	for _, tc := range []struct {
		exit     int
		rollback bool
	}{{134, false}, {137, false}, {1, false}, {2, false}, {126, true}, {127, true}, {132, true}, {139, true}} {
		in, s := installer(t, claudeLatest())
		fakeInstall(t, s, "claude", "2.1.311")
		_ = s.Use("claude", "2.1.311")
		syncClaude(t, in, SyncOptions{})

		rt := Wrap(&recordingRuntime{exit: tc.exit}, s)
		for i := 0; i < 4; i++ {
			_, _ = rt.Run(context.Background(), sandbox.CommandRequest{Command: []string{"bash", "-lc", "claude -p 'huge refactor'"}})
		}
		rolled := current(t, s, "claude") == "2.1.311"
		if rolled != tc.rollback {
			t.Errorf("exit %d: rolled back = %v, want %v", tc.exit, rolled, tc.rollback)
		}
	}
}

// ClearBad re-allows a version; it does not switch to it. After a rollback current is an older
// version, and Sync never moves a tool that is not on the newest good version, so the operator
// switches with Use.
func TestStore_ClearBadReallowsTheVersionButDoesNotSwitch(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	syncClaude(t, in, SyncOptions{}) // 2.1.312 current
	if _, err := s.Rollback("claude", "judged wrongly"); err != nil {
		t.Fatal(err)
	}
	syncClaude(t, in, SyncOptions{})
	if current(t, s, "claude") != "2.1.311" {
		t.Fatalf("a bad release came back: %q", current(t, s, "claude"))
	}

	if err := s.ClearBad("claude", "2.1.312"); err != nil {
		t.Fatal(err)
	}
	if bad, _ := s.BadVersions("claude"); len(bad) != 0 {
		t.Errorf("bad = %v after ClearBad", bad)
	}
	syncClaude(t, in, SyncOptions{})
	if current(t, s, "claude") != "2.1.311" {
		t.Errorf("after ClearBad Sync moved current to %q; switching is the operator's call", current(t, s, "claude"))
	}
	if err := s.Use("claude", "2.1.312"); err != nil {
		t.Fatalf("the cleared version cannot be used: %v", err)
	}
	// from there on Sync follows new releases again
	f := claudeLatest()
	f.latest, f.versionOut = "2.1.313", "2.1.313 (Claude Code)"
	in.Runtime = f
	syncClaude(t, in, SyncOptions{})
	if current(t, s, "claude") != "2.1.313" {
		t.Errorf("after use, a newer release was not followed: %q", current(t, s, "claude"))
	}
	if err := s.ClearBad("claude", "9.9.9"); err != nil {
		t.Errorf("clearing a version that is not marked = %v, want a no-op", err)
	}
}

func TestStore_ClearBadKeepsTheOtherMarks(t *testing.T) {
	s := newStore(t)
	_ = s.MarkBad("claude", "2.1.310", "a")
	_ = s.MarkBad("claude", "2.1.312", "b")
	if err := s.ClearBad("claude", "2.1.310"); err != nil {
		t.Fatal(err)
	}
	bad, _ := s.BadVersions("claude")
	if len(bad) != 1 || bad["2.1.312"] != "b" {
		t.Errorf("bad = %v, want only 2.1.312", bad)
	}
}

// Going back to a version that is itself marked bad would swap one known problem for another.
func TestStore_RollbackRefusesAVersionMarkedBad(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "claude", "2.1.310")
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311")
	_ = s.Use("claude", "2.1.310") // previous = 2.1.311
	_ = s.MarkBad("claude", "2.1.311", "known broken")

	_, err := s.Rollback("claude", "x")
	if !errors.Is(err, ErrBadVersion) || !strings.Contains(err.Error(), "known broken") {
		t.Fatalf("err = %v, want ErrBadVersion with the reason", err)
	}
	if current(t, s, "claude") != "2.1.310" {
		t.Errorf("a refused rollback changed current to %q", current(t, s, "claude"))
	}
	if bad, _ := s.BadVersions("claude"); bad["2.1.310"] != "" {
		t.Errorf("a refused rollback marked %v bad", bad)
	}
}
