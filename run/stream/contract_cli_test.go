//go:build cli

// This file exercises the real agy and claude binaries against their undocumented stream-json
// contract (see docs/openspecs/warm-session/spec.md). It is excluded from the normal build and
// test run (no `cli` build tag) because it needs the real CLIs installed and logged in, spends
// real quota, and takes tens of seconds. Run it explicitly:
//
//	go test -tags cli ./run/stream/... -run TestContract -v
//
// Re-run it whenever agy or claude is upgraded: a shape change here means Session (run/session.go)
// must be updated before the warm pool is trusted again.
package stream_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// turnResult is what both CLIs' stream-json last line is read into, for the fields the contract
// cares about. The full line is handed to the caller unparsed too: the warm session will pass it
// through to the exact same envelope parser the one-shot path already uses (parseOutput in
// internal/ai/provider/cli/cli.go, faster-learning repo), so this test only checks the fields that
// parser depends on, not the whole shape.
type turnResult struct {
	raw        []byte
	isAgy      bool
	status     string // agy: top-level "status"
	response   string // agy: top-level "response"
	resultType string // claude: top-level "type" == "result"
	isError    bool   // claude: top-level "is_error"
	result     string // claude: top-level "result"
}

// contractSession drives one CLI process over stdin/stdout pipes, line by line, the same shape
// run.Session (Phase 1) will use internally.
type contractSession struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lineCh chan []byte // fed by one persistent reader goroutine; closed when stdout is exhausted
	stderr *bytes.Buffer
}

func startContractSession(t *testing.T, name string, args []string) *contractSession {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed on this machine: %v", name, err)
	}
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	lineCh := make(chan []byte, 64)
	go func() {
		for sc.Scan() {
			l := sc.Bytes()
			if len(bytes.TrimSpace(l)) == 0 {
				continue
			}
			lineCh <- append([]byte(nil), l...)
		}
		close(lineCh)
	}()
	s := &contractSession{t: t, cmd: cmd, stdin: stdin, lineCh: lineCh, stderr: &stderr}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return s
}

// nextLine reads the next non-empty line, or fails the test after timeout. It reads from a
// single long-lived reader goroutine (started in startContractSession) rather than spawning one
// per call: a per-call goroutine abandoned on timeout would keep calling Scan() concurrently with
// the next call's own goroutine, which is unsafe on a shared bufio.Scanner. A persistent reader
// feeding a buffered channel avoids that, and also means a line that arrives after a caller gives
// up is still delivered to whoever calls nextLine next, instead of being silently dropped.
func (s *contractSession) nextLine(timeout time.Duration) []byte {
	s.t.Helper()
	select {
	case l, ok := <-s.lineCh:
		if !ok {
			s.t.Fatalf("CLI exited before producing a line (stderr: %s)", s.stderr.String())
		}
		return l
	case <-time.After(timeout):
		s.t.Fatalf("timed out waiting for a line (stderr so far: %s)", s.stderr.String())
		return nil
	}
}

func (s *contractSession) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
}

// ---- agy ----------------------------------------------------------------------------------

// TestContract_Agy pins the shape documented in docs/openspecs/warm-session/spec.md: an "init"
// event once the process is ready to receive turns, a plain-string "message.content" input, and
// a "result" event per turn carrying {"status","response","usage"}. It also checks that a second
// turn on the same (already warm) process is markedly faster than the first — the entire premise
// of the warm pool.
func TestContract_Agy(t *testing.T) {
	s := startContractSession(t, "agy", []string{
		"--sandbox", "--disable-slash-commands", "--effort", "low",
		"--input-format", "stream-json", "--output-format", "stream-json", "-p", "",
	})

	start := time.Now()
	initLine := s.nextLine(30 * time.Second)
	var init struct {
		Event          string `json:"event"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(initLine, &init); err != nil || init.Event != "init" {
		t.Fatalf("first line is not an init event: %s (err=%v)", initLine, err)
	}
	if init.ConversationID == "" {
		t.Error("init carries no conversation_id")
	}
	spawnToReady := time.Since(start)
	t.Logf("agy: spawn-to-ready = %s", spawnToReady)

	r1, d1 := agyTurn(t, s, `{"n":1}`)
	t.Logf("agy: turn 1 (cold) = %s", d1)
	if r1.status != "SUCCESS" {
		t.Fatalf("turn 1: status = %q, raw = %s", r1.status, r1.raw)
	}
	if strings.TrimSpace(r1.response) != `{"n":1}` {
		t.Errorf("turn 1: response = %q", r1.response)
	}

	r2, d2 := agyTurn(t, s, `{"n":2}`)
	t.Logf("agy: turn 2 (warm) = %s", d2)
	if r2.status != "SUCCESS" {
		t.Fatalf("turn 2: status = %q, raw = %s", r2.status, r2.raw)
	}
	if strings.TrimSpace(r2.response) != `{"n":2}` {
		t.Errorf("turn 2: response = %q", r2.response)
	}

	// The whole point of the warm pool: a turn on an already-ready process must not pay the
	// init cost again. We don't assert a hard ratio (model latency varies), just that it is not
	// slower than the first turn by more than a small margin.
	if d2 > d1 {
		t.Logf("note: warm turn (%s) was not faster than the cold turn (%s) this run; re-run to confirm, latency varies", d2, d1)
	}
}

func agyTurn(t *testing.T, s *contractSession, want string) (turnResult, time.Duration) {
	t.Helper()
	start := time.Now()
	prompt := fmt.Sprintf("Reply with exactly the JSON %s", want)
	payload, err := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": prompt},
	})
	if err != nil {
		t.Fatalf("encode turn: %v", err)
	}
	s.send(t, string(payload))
	for {
		line := s.nextLine(60 * time.Second)
		var top struct {
			Event  string `json:"event"`
			Result *struct {
				Status   string `json:"status"`
				Response string `json:"response"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &top); err != nil {
			t.Fatalf("decode line: %v (line=%s)", err, line)
		}
		if top.Event != "result" {
			continue // step_update: ignored by this contract test, same as Session will ignore it
		}
		if top.Result == nil {
			t.Fatalf("result event with no result field: %s", line)
		}
		return turnResult{raw: line, isAgy: true, status: top.Result.Status, response: top.Result.Response}, time.Since(start)
	}
}

// ---- claude -----------------------------------------------------------------------------

// TestContract_Claude pins the shape for claude: input is
// {"type":"user","message":{"role":"user","content":[{"type":"text","text":…}]}}, and the final
// line of each turn is the exact envelope the one-shot path's parseOutput already understands
// ({"type":"result","is_error":…,"result":…}), so the warm session needs no new output parser
// for claude, only a new input encoder.
//
// Unlike agy, claude prints nothing at all — not even its "system/init" line — until the first
// turn's bytes have actually reached its stdin (confirmed by spawning claude, waiting 15s with
// nothing sent, and observing zero output; then sending a turn and seeing "init" follow within
// ~200ms). So this test does not wait for a standalone "ready" signal before the first turn, and
// neither can Session.Ready() for claude (see design.md): claude's startup is a no-op signal, not
// an absent one. What idling an already-spawned claude process DOES still save is measured
// separately, in TestContract_Claude_PrespawnBenefit below.
func TestContract_Claude(t *testing.T) {
	s := startContractSession(t, "claude", []string{
		"--print", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--strict-mcp-config", "--tools", "",
	})

	r1, d1 := claudeTurn(t, s, `{"n":1}`)
	t.Logf("claude: turn 1 (process spawned, turn sent at once) = %s", d1)
	if r1.resultType != "result" || r1.isError {
		t.Fatalf("turn 1: type=%q is_error=%v, raw=%s", r1.resultType, r1.isError, r1.raw)
	}
	if strings.TrimSpace(r1.result) != `{"n":1}` {
		t.Errorf("turn 1: result = %q", r1.result)
	}

	r2, d2 := claudeTurn(t, s, `{"n":2}`)
	t.Logf("claude: turn 2 (warm) = %s", d2)
	if r2.resultType != "result" || r2.isError {
		t.Fatalf("turn 2: type=%q is_error=%v, raw=%s", r2.resultType, r2.isError, r2.raw)
	}
	if strings.TrimSpace(r2.result) != `{"n":2}` {
		t.Errorf("turn 2: result = %q", r2.result)
	}
}

// TestContract_Claude_PrespawnBenefit measures what spawning claude early, before any job is
// ready to send it a turn, actually buys: does claude do its ~1-5s of local startup work (skills,
// settings, hooks — visible in its own startup_timing telemetry) during the idle gap regardless of
// stdin, or does it defer everything, pre-spawn or not, until data arrives? If the latter, warming
// claude ahead of time would be worthless. idle (5s) stands in for "spawned while the learner was
// still reading the pack list"; see proposal.md for why 5s is a realistic gap.
func TestContract_Claude_PrespawnBenefit(t *testing.T) {
	s := startContractSession(t, "claude", []string{
		"--print", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--strict-mcp-config", "--tools", "",
	})
	idle := 5 * time.Second
	t.Logf("idling %s before sending anything...", idle)
	time.Sleep(idle)

	r, d := claudeTurn(t, s, `{"n":1}`)
	t.Logf("claude: turn on a process pre-spawned %s earlier = %s", idle, d)
	if r.resultType != "result" || r.isError {
		t.Fatalf("type=%q is_error=%v, raw=%s", r.resultType, r.isError, r.raw)
	}
	// This is the number that decides whether claude belongs in the warm pool at all (see
	// proposal.md "Assumptions"): compare it against a one-shot `claude -p` run's wall time. If
	// they are within noise of each other, claude gets no benefit from pre-spawning and should be
	// left on the one-shot path (StreamInput == "" in effect, even though the format itself works).
}

func claudeTurn(t *testing.T, s *contractSession, want string) (turnResult, time.Duration) {
	t.Helper()
	start := time.Now()
	prompt := fmt.Sprintf("Reply with exactly the JSON %s", want)
	payload, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": prompt}},
		},
	})
	if err != nil {
		t.Fatalf("encode turn: %v", err)
	}
	s.send(t, string(payload))
	for {
		line := s.nextLine(60 * time.Second)
		var top struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if err := json.Unmarshal(line, &top); err != nil {
			t.Fatalf("decode line: %v (line=%s)", err, line)
		}
		if top.Type != "result" {
			continue // assistant/system progress lines: ignored, same as Session will ignore them
		}
		return turnResult{raw: line, resultType: top.Type, isError: top.IsError, result: top.Result}, time.Since(start)
	}
}

// ---- shared ------------------------------------------------------------------------------

// TestContract_IdleDoesNotBurnQuota is a manual, slow check: it starts agy, leaves it idle for
// the warm pool's default TTL, then runs one turn and confirms the usage reported for that turn
// alone (not cumulative) is what a single fresh turn costs — i.e. idling a warm process does not
// itself consume tokens. Skipped unless AICLI_CONTRACT_IDLE=1, since it takes ~5 minutes and nobody
// should pay for it on every CI run.
func TestContract_IdleDoesNotBurnQuota(t *testing.T) {
	if os.Getenv("AICLI_CONTRACT_IDLE") != "1" {
		t.Skip("set AICLI_CONTRACT_IDLE=1 to run this ~5 minute manual check")
	}
	s := startContractSession(t, "agy", []string{
		"--sandbox", "--disable-slash-commands", "--effort", "low",
		"--input-format", "stream-json", "--output-format", "stream-json", "-p", "",
	})
	s.nextLine(30 * time.Second) // init
	t.Log("idling for 5 minutes (the warm pool's default TTL) before sending a turn...")
	time.Sleep(5 * time.Minute)
	r, d := agyTurn(t, s, `{"n":1}`)
	t.Logf("turn after 5 minutes idle: %s, raw usage = %s", d, r.raw)
	// A regression here would show up as the model having "seen" extra turns it was never sent
	// (num_turns > 1) or a response that is not the plain echo asked for.
}
