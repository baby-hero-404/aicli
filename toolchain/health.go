package toolchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoPrevious is returned by Rollback when there is no earlier version to go back to.
var ErrNoPrevious = errors.New("toolchain: no previous version to roll back to")

// ErrBadVersion is returned by Rollback when the version it would go back to is itself marked bad.
// Switch to a good version with Use, or clear the mark with ClearBad if it was a mistake.
var ErrBadVersion = errors.New("toolchain: version is marked bad")

// Defaults of the automatic rollback.
const (
	// DefaultProbation is how long after Sync made a release current that failures of that release
	// are held against it.
	DefaultProbation = 24 * time.Hour
	// DefaultMaxFailures is how many failures in a row, without a success between, roll it back.
	DefaultMaxFailures = 3
)

// HealthConfig tunes the automatic rollback of a release that Sync made current.
type HealthConfig struct {
	// Probation is the window after the switch in which failures count (default DefaultProbation).
	Probation time.Duration
	// MaxFailures failures in a row roll the release back (default DefaultMaxFailures).
	MaxFailures int
	// Now is for tests.
	Now func() time.Time
}

func (c HealthConfig) withDefaults() HealthConfig {
	if c.Probation <= 0 {
		c.Probation = DefaultProbation
	}
	if c.MaxFailures <= 0 {
		c.MaxFailures = DefaultMaxFailures
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// RollbackEvent describes a rollback.
type RollbackEvent struct {
	Tool   string
	From   string // the version that was current and is now marked bad
	To     string // the version current again; "" means the CLI baked into the image
	Reason string
}

// autoState is the release Sync last made current, and how it is doing.
type autoState struct {
	Version  string    `json:"version"`
	At       time.Time `json:"at"`
	Failures int       `json:"failures"` // in a row
}

func (s *Store) toolFile(name, file string) string { return filepath.Join(s.root, name, file) }

// switchTo makes version current (it must be installed, or "" for the image's CLI). With
// recordPrevious it remembers what was current, for Rollback; without, it forgets it, because a
// rollback must not make the version it left the target of the next one. Callers hold s.mu.
func (s *Store) switchTo(name, version string, recordPrevious bool) error {
	cur, _ := s.Current(name)
	if version == "" {
		if err := os.Remove(s.toolFile(name, "current")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("toolchain: clear current: %w", err)
		}
	} else if err := writeAtomic(s.toolFile(name, "current"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	switch {
	case !recordPrevious:
		if err := os.Remove(s.toolFile(name, "previous")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("toolchain: forget previous: %w", err)
		}
	case cur != version:
		// An empty file means "the image's CLI was current": it is a valid thing to go back to.
		return writeAtomic(s.toolFile(name, "previous"), []byte(cur+"\n"), 0o644)
	}
	return nil
}

// Previous returns the version that was current before the last switch. ok is false when none is
// recorded; "" with ok true means the CLI baked into the image.
func (s *Store) Previous(name string) (version string, ok bool, err error) {
	if _, err := lookup(name); err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(s.toolFile(name, "previous"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("toolchain: read previous: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if v != "" && checkVersion(v) != nil {
		return "", false, fmt.Errorf("toolchain: %s/previous holds an invalid version %q", name, v)
	}
	return v, true, nil
}

// BadVersions returns the versions marked bad, with the reason. Sync never makes one current
// again.
func (s *Store) BadVersions(name string) (map[string]string, error) {
	if _, err := lookup(name); err != nil {
		return nil, err
	}
	bad := map[string]string{}
	b, err := os.ReadFile(s.toolFile(name, "bad.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return bad, nil
	}
	if err != nil {
		return nil, fmt.Errorf("toolchain: read bad versions: %w", err)
	}
	if err := json.Unmarshal(b, &bad); err != nil {
		return nil, fmt.Errorf("toolchain: %s/bad.json: %w", name, err)
	}
	return bad, nil
}

// ClearBad removes the bad mark of a version (a rollback judged it wrongly, or it was fixed), so
// Sync may make it current again. It is a no-op for a version that is not marked.
func (s *Store) ClearBad(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bad, err := s.BadVersions(name)
	if err != nil {
		return err
	}
	if _, ok := bad[version]; !ok {
		return nil
	}
	delete(bad, version)
	if len(bad) == 0 {
		if err := os.Remove(s.toolFile(name, "bad.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("toolchain: clear bad: %w", err)
		}
		return nil
	}
	b, err := json.MarshalIndent(bad, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.toolFile(name, "bad.json"), b, 0o644)
}

// MarkBad records that a version must not be made current automatically.
func (s *Store) MarkBad(name, version, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markBad(name, version, reason)
}

func (s *Store) markBad(name, version, reason string) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	bad, err := s.BadVersions(name)
	if err != nil {
		return err
	}
	bad[version] = reason
	b, err := json.MarshalIndent(bad, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.toolFile(name, "bad.json"), b, 0o644)
}

// Rollback goes back to the version that was current before the last switch and marks the one it
// leaves as bad, so Sync does not bring it back. It fails with ErrNoPrevious when no earlier
// version is recorded, and with ErrNotInstalled when it was removed.
func (s *Store) Rollback(name, reason string) (RollbackEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rollback(name, reason)
}

func (s *Store) rollback(name, reason string) (RollbackEvent, error) {
	from, err := s.Current(name)
	if err != nil {
		return RollbackEvent{}, err
	}
	to, ok, err := s.Previous(name)
	if err != nil {
		return RollbackEvent{}, err
	}
	if !ok || from == "" {
		return RollbackEvent{}, fmt.Errorf("%w: %s", ErrNoPrevious, name)
	}
	if to != "" {
		if installed, err := s.Installed(name, to); err != nil {
			return RollbackEvent{}, err
		} else if !installed {
			return RollbackEvent{}, fmt.Errorf("%w: %s@%s", ErrNotInstalled, name, to)
		}
		bad, err := s.BadVersions(name)
		if err != nil {
			return RollbackEvent{}, err
		}
		if reason, isBad := bad[to]; isBad {
			return RollbackEvent{}, fmt.Errorf("%w: %s@%s (%s); use another version", ErrBadVersion, name, to, reason)
		}
	}
	if err := s.markBad(name, from, reason); err != nil {
		return RollbackEvent{}, err
	}
	if err := s.switchTo(name, to, false); err != nil {
		return RollbackEvent{}, err
	}
	s.clearProbation(name)
	return RollbackEvent{Tool: name, From: from, To: to, Reason: reason}, nil
}

// useOnProbation makes version current and puts it on probation: failures reported for it
// (ReportOutcome) can roll it back. It is how Sync switches.
func (s *Store) useOnProbation(name, version string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeAuto(name, autoState{Version: version, At: now}); err != nil {
		return err
	}
	if err := s.switchTo(name, version, true); err != nil {
		s.clearProbation(name)
		return err
	}
	return nil
}

// clearProbation forgets the release on probation. Callers hold s.mu.
func (s *Store) clearProbation(name string) {
	_ = os.Remove(s.toolFile(name, "auto.json"))
}

func (s *Store) writeAuto(name string, a autoState) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return writeAtomic(s.toolFile(name, "auto.json"), b, 0o644)
}

func (s *Store) readAuto(name string) (autoState, bool) {
	b, err := os.ReadFile(s.toolFile(name, "auto.json"))
	if err != nil {
		return autoState{}, false
	}
	var a autoState
	if json.Unmarshal(b, &a) != nil {
		return autoState{}, false
	}
	return a, true
}

// ReportOutcome tells the store how a run with the tool at version went. It only matters for a
// release Sync made current, during its probation: a success resets the failure count, and
// MaxFailures failures in a row roll the release back to the previous version at once (without
// waiting for the next Sync) and mark it bad. It returns the rollback if one happened. A version
// that is not the one on probation, or an operator's own choice, is never rolled back.
func (s *Store) ReportOutcome(name, version string, ok bool, reason string, cfg HealthConfig) (*RollbackEvent, error) {
	cfg = cfg.withDefaults()
	s.mu.Lock()
	defer s.mu.Unlock()
	a, onProbation := s.readAuto(name)
	if !onProbation || a.Version != version || cfg.Now().Sub(a.At) > cfg.Probation {
		return nil, nil
	}
	if cur, err := s.Current(name); err != nil || cur != version {
		return nil, err
	}
	if ok {
		if a.Failures == 0 {
			return nil, nil
		}
		a.Failures = 0
		return nil, s.writeAuto(name, a)
	}
	a.Failures++
	if a.Failures < cfg.MaxFailures {
		return nil, s.writeAuto(name, a)
	}
	ev, err := s.rollback(name, fmt.Sprintf("%d failures in a row after the switch: %s", a.Failures, reason))
	if err != nil {
		return nil, err
	}
	return &ev, nil
}
