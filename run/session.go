package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/sandbox"
)

// ErrSessionUsed is returned by Session.Turn when called more than once on the same Session: a
// warm session serves exactly one job, never two. The CLI itself keeps conversational context
// across turns written to the same stdin (confirmed for both agy and claude by the contract test
// in run/stream/contract_cli_test.go — a second turn's token usage included the first), so
// reusing a Session would let one job's data leak into the next and its prompt grow unbounded.
// This is enforced here, in the library, not left to callers to get right.
var ErrSessionUsed = errors.New("run: session already served a turn")

// SessionRequest describes one warm CLI process: started before there is a prompt to send, kept
// open, and asked for exactly one turn. It mirrors Request's shape — Command and Args are the
// caller's own vetted invocation, never auto-filled from the profile registry, same convention
// as Request.Command/Args — but carries no Prompt (the turn goes over stdin, not a prompt file),
// no CaptureFiles, Setup/Cleanup or OnEvent: those are one-shot-run concerns a session has no use
// for.
type SessionRequest struct {
	// Profile selects the turn encoding (profile.Profile.SessionInput) and the session/env
	// plumbing (StateEnv, SessionDir mounts). It must name a known profile with SessionInput
	// set, or StartSession returns profile.ErrStreamingUnsupported.
	Profile string
	// Command and Args are the exact invocation to start.
	Command string
	Args    []string

	Workspace       string
	Env             map[string]string
	SessionDir      string
	SessionMounts   map[string]string
	CredentialFiles map[string]string
	// IdleTimeout bounds how long the process may sit with no output once a turn has been sent
	// (passed straight to the runtime, same meaning as Request.IdleTimeout). It does not apply
	// while a Session is simply waiting, unused, for its first turn — nothing is running yet to
	// go idle; how long that wait is worth is the caller's call (a pool's TTL), not a timeout
	// this request could express.
	IdleTimeout time.Duration
	TaskID      string
}

// readyTimeout bounds Session.Ready for a profile that has a live readiness line to wait for
// (only agy today). It is short because the line is printed as soon as the process can accept
// input at all — Phase 0 measured well under a second once the process is actually running; a
// much longer wait would just mean something is already wrong.
const readyTimeout = 30 * time.Second

// Session is one warm, single-use CLI process started by Runner.StartSession. The zero value is
// not usable; only a value returned by StartSession is.
type Session struct {
	proc  sandbox.Process
	lines *lineReader
	kind  string // profile.Profile.SessionInput: "agy" | "claude"
	ref   string // profile ref, for error messages

	mu   sync.Mutex
	used bool
	done bool
}

// registrySession is registryRun for a session: it reports req.Profile only when the invocation
// is exactly the built-in profile's own SessionArgs, the same security invariant registryRun
// gives Run (a runtime without isolation only trusts an exact, vetted invocation).
func registrySession(req SessionRequest) string {
	p, ok := profile.Lookup(req.Profile)
	if !ok || req.Command != p.Command || !equalStrings(req.Args, p.SessionArgs) {
		return ""
	}
	return req.Profile
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// StartSession starts req's command under the runtime's own isolation (the same private
// HOME/credential files one request gets from Run, see sandbox.SessionRuntime) and returns once
// the process has been launched — not once it is ready for a turn; call Ready for that.
func (r *Runner) StartSession(ctx context.Context, req SessionRequest) (*Session, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	p, ok := profile.Lookup(req.Profile)
	if !ok || p.SessionInput == "" {
		return nil, fmt.Errorf("%w: %q", profile.ErrStreamingUnsupported, req.Profile)
	}
	sr, ok := r.Runtime.(sandbox.SessionRuntime)
	if !ok {
		return nil, fmt.Errorf("%w: runtime does not support warm sessions", ErrInvalidRequest)
	}
	layout, err := r.layout(Request{TaskID: req.TaskID, Workspace: req.Workspace})
	if err != nil {
		return nil, err
	}
	mounts, env, err := sessionLayout(Request{
		Profile: req.Profile, SessionDir: req.SessionDir, SessionMounts: req.SessionMounts, Env: req.Env,
	}, layout)
	if err != nil {
		return nil, err
	}
	sreq := sandbox.CommandRequest{
		TaskID: req.TaskID, Workspace: req.Workspace,
		Command: append([]string{req.Command}, req.Args...),
		Env:     env, CredentialFiles: credentialTargets(layout.Home, req.CredentialFiles),
		SessionMounts: mounts, IdleTimeout: req.IdleTimeout, Profile: registrySession(req),
	}
	proc, err := sr.Start(ctx, sreq)
	if err != nil {
		return nil, fmt.Errorf("run: start session: %w", err)
	}
	return &Session{proc: proc, lines: newLineReader(proc.Stdout()), kind: p.SessionInput, ref: req.Profile}, nil
}

// Ready waits until the process can be given its first turn. For a profile with a live
// readiness line (agy: {"event":"init",…}), it waits for exactly that line. For a profile with
// none (claude: confirmed by the contract test to print nothing at all, not even that it has
// started, until a turn's bytes reach its stdin — see run/stream/contract_cli_test.go), Ready
// returns at once: Start already confirmed the process launched, and there is no further signal
// to wait for. Call it at most once; calling it again after Turn has read the init line would
// hang waiting for a line that will not come again.
func (s *Session) Ready(ctx context.Context) error {
	if s.kind != "agy" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	line, err := s.lines.next(ctx)
	if err != nil {
		return fmt.Errorf("run: session %q not ready: %w", s.ref, err)
	}
	var top struct {
		Event string `json:"event"`
	}
	if json.Unmarshal(line, &top) != nil || top.Event != "init" {
		return fmt.Errorf("run: session %q's first line was not an init event: %s", s.ref, RedactSecrets(string(line)))
	}
	return nil
}

// TurnResult is what Session.Turn gives back: the CLI's own raw result line, unparsed, and how
// long the turn took. Output is handed on exactly as the one-shot path's combined process output
// is — the caller applies the same envelope parser and auth/quota detection it already has for
// Run, rather than Session re-implementing that classification over a single line with no exit
// code or full stderr to go on.
type TurnResult struct {
	Output  []byte
	Latency time.Duration
}

// Turn sends prompt as the session's one and only turn and waits for its result. ctx bounds only
// this call: it does not reach back to cancel Start, and it is not the same as IdleTimeout, which
// the runtime enforces independently of whatever deadline ctx carries.
//
// A second call returns ErrSessionUsed without touching the process — the session is still open
// and must still be closed by the caller.
func (s *Session) Turn(ctx context.Context, prompt string) (*TurnResult, error) {
	s.mu.Lock()
	if s.used {
		s.mu.Unlock()
		return nil, ErrSessionUsed
	}
	s.used = true
	s.mu.Unlock()

	start := time.Now()
	line, err := encodeTurn(s.kind, prompt)
	if err != nil {
		return nil, err
	}
	if _, err := s.proc.Stdin().Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("run: write turn to session %q: %w", s.ref, err)
	}
	for {
		l, err := s.lines.next(ctx)
		if err != nil {
			return nil, fmt.Errorf("run: session %q ended before a result: %w", s.ref, err)
		}
		raw, isResult := matchResult(s.kind, l)
		if !isResult {
			continue // a progress line (agy step_update, claude assistant/system/rate_limit_event…): not the turn's own result, keep reading
		}
		return &TurnResult{Output: raw, Latency: time.Since(start)}, nil
	}
}

// Close ends the session, whether or not Turn was ever called: it closes Stdin (signalling EOF,
// the polite way to end a CLI waiting for its next turn) and gives the process a moment to exit
// on its own before killing it. Calling Close more than once is a no-op after the first.
func (s *Session) Close() (*sandbox.CommandResult, error) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil, nil
	}
	s.done = true
	s.mu.Unlock()

	_ = s.proc.Stdin().Close()
	type outcome struct {
		res *sandbox.CommandResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := s.proc.Wait()
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-time.After(2 * time.Second):
		_ = s.proc.Kill("session closed")
		o := <-done
		return o.res, o.err
	}
}

// encodeTurn builds the one NDJSON line a profile's SessionInput kind expects for prompt. Pinned
// by the contract test; re-check it after a CLI upgrade before trusting a change here.
func encodeTurn(kind, prompt string) ([]byte, error) {
	switch kind {
	case "agy":
		return json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": prompt}})
	case "claude":
		return json.Marshal(map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": prompt}},
			},
		})
	}
	return nil, fmt.Errorf("run: unknown session input kind %q", kind)
}

// matchResult reports whether line is the terminal line of a turn for kind, returning it
// unmodified: the caller's own envelope parser (the one it already has for the one-shot path's
// output) reads it from here. A line that does not parse, or parses but is not the terminal kind,
// is not an error — it is simply not the result yet, and Turn keeps reading.
func matchResult(kind string, line []byte) (raw []byte, isResult bool) {
	switch kind {
	case "agy":
		var top struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &top) == nil && top.Event == "result" {
			return line, true
		}
	case "claude":
		var top struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &top) == nil && top.Type == "result" {
			return line, true
		}
	}
	return nil, false
}

// lineReader reads complete, non-blank lines from r on a single long-lived goroutine, delivering
// them over a channel. A per-call goroutine (one spawned fresh each time the caller wants the
// next line, abandoned on timeout) is unsafe here: an abandoned goroutine left behind by a timed
// out call keeps calling bufio.Scanner.Scan concurrently with the next call's own goroutine,
// which is not safe on a shared Scanner — this is exactly what made the contract test hang under
// go test the first time it was written (see docs/openspecs/warm-session/spec.md). One persistent
// reader avoids that, and a line that arrives after a caller gives up still reaches whoever asks
// next instead of being silently dropped.
type lineReader struct {
	ch chan []byte
}

func newLineReader(r interface{ Read([]byte) (int, error) }) *lineReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	lr := &lineReader{ch: make(chan []byte, 64)}
	go func() {
		for sc.Scan() {
			l := sc.Bytes()
			if len(bytes.TrimSpace(l)) == 0 {
				continue
			}
			lr.ch <- append([]byte(nil), l...)
		}
		close(lr.ch)
	}()
	return lr
}

// next returns the next line, or an error if the stream ended (io.EOF-shaped: no more lines will
// ever come) or ctx was done first.
func (lr *lineReader) next(ctx context.Context) ([]byte, error) {
	select {
	case l, ok := <-lr.ch:
		if !ok {
			return nil, errors.New("run: session output ended")
		}
		return l, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
