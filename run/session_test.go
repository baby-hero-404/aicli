package run

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// safeBuffer is a concurrency-safe io.WriteCloser standing in for a process's real stdin pipe:
// Session.Turn writes exactly one line to it and the test inspects what was written. A real
// io.Pipe would also work but needs a concurrent reader or every write blocks; a session's turn
// is exactly one write, so capturing it plainly is simpler and no less faithful.
type safeBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeProcess implements sandbox.Process for Session tests. Stdout is a real io.Pipe: a test
// scripts what the "CLI" says by writing to stdoutW, letting Session.Ready/Turn block on it
// exactly as they would on a real process's pipe.
type fakeProcess struct {
	stdin         *safeBuffer
	stdoutR       *io.PipeReader
	stdoutW       *io.PipeWriter
	waitRes       *sandbox.CommandResult
	waitErr       error
	waitBlockedOn chan struct{} // non-nil: Wait blocks until this is closed

	mu     sync.Mutex
	killed []string
}

func newFakeProcess() *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{stdin: &safeBuffer{}, stdoutR: r, stdoutW: w, waitRes: &sandbox.CommandResult{}}
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeProcess) Stdout() io.Reader     { return p.stdoutR }

func (p *fakeProcess) Kill(reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = append(p.killed, reason)
	return nil
}

func (p *fakeProcess) Wait() (*sandbox.CommandResult, error) {
	if p.waitBlockedOn != nil {
		<-p.waitBlockedOn
	}
	return p.waitRes, p.waitErr
}

// writeLine writes one NDJSON line to the fake process's stdout, as the CLI would.
func (p *fakeProcess) writeLine(line string) {
	_, _ = io.WriteString(p.stdoutW, line+"\n")
}

// fakeSessionRuntime implements sandbox.Runtime (embedding StubRuntime, unused by these tests)
// and sandbox.SessionRuntime, handing back a scripted fakeProcess and recording the request it
// was asked to start.
type fakeSessionRuntime struct {
	sandbox.StubRuntime
	proc *fakeProcess
	err  error
	req  sandbox.CommandRequest
}

func (f *fakeSessionRuntime) Start(_ context.Context, req sandbox.CommandRequest) (sandbox.Process, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}

var _ sandbox.SessionRuntime = (*fakeSessionRuntime)(nil)

func agySessionReq(t *testing.T) SessionRequest {
	t.Helper()
	return SessionRequest{
		Profile: "antigravity_notools", Command: "agy",
		Args:      []string{"--sandbox", "--disable-slash-commands", "--input-format", "stream-json", "--output-format", "stream-json", "-p", ""},
		Workspace: t.TempDir(),
	}
}

func claudeSessionReq(t *testing.T) SessionRequest {
	t.Helper()
	return SessionRequest{
		Profile: "claude_code_notools", Command: "claude",
		Args:      []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--strict-mcp-config", "--tools", ""},
		Workspace: t.TempDir(),
	}
}

func TestStartSession_UnknownOrNonStreamingProfile(t *testing.T) {
	rt := &fakeSessionRuntime{proc: newFakeProcess()}
	r := &Runner{Runtime: rt}
	for _, profile := range []string{"no-such-profile", "claude_code", "openai_codex"} {
		req := agySessionReq(t)
		req.Profile = profile
		if _, err := r.StartSession(context.Background(), req); err == nil {
			t.Errorf("profile %q: expected an error (unknown, or no SessionInput)", profile)
		}
	}
}

func TestStartSession_RuntimeWithoutSessionSupport(t *testing.T) {
	r := &Runner{Runtime: &sandbox.StubRuntime{}} // does not implement sandbox.SessionRuntime
	if _, err := r.StartSession(context.Background(), agySessionReq(t)); err == nil {
		t.Error("expected an error when the runtime cannot start a session")
	}
}

func TestStartSession_MissingCommand(t *testing.T) {
	rt := &fakeSessionRuntime{proc: newFakeProcess()}
	r := &Runner{Runtime: rt}
	req := agySessionReq(t)
	req.Command = ""
	if _, err := r.StartSession(context.Background(), req); err == nil {
		t.Error("expected an error for a missing command")
	}
}

func TestStartSession_RegistryProfileOnlyOnExactInvocation(t *testing.T) {
	rt := &fakeSessionRuntime{proc: newFakeProcess()}
	r := &Runner{Runtime: rt}

	req := agySessionReq(t)
	if _, err := r.StartSession(context.Background(), req); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if rt.req.Profile != "antigravity_notools" {
		t.Errorf("exact invocation: sandbox request Profile = %q, want the built-in ref", rt.req.Profile)
	}

	req2 := agySessionReq(t)
	req2.Args = append(req2.Args, "--extra")
	rt2 := &fakeSessionRuntime{proc: newFakeProcess()}
	if _, err := (&Runner{Runtime: rt2}).StartSession(context.Background(), req2); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if rt2.req.Profile != "" {
		t.Errorf("a changed invocation must not be trusted as the built-in profile, got %q", rt2.req.Profile)
	}
}

func TestSession_Agy_ReadyWaitsForInit(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	proc.writeLine(`{"event":"init","conversation_id":"abc"}`)
	if err := s.Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestSession_Agy_ReadyRejectsAnythingElse(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	proc.writeLine(`{"event":"result","result":{"status":"SUCCESS"}}`)
	if err := s.Ready(context.Background()); err == nil {
		t.Error("Ready must reject a first line that is not init")
	}
}

func TestSession_Agy_ReadyTimesOut(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Ready(ctx); err == nil {
		t.Error("Ready must time out when nothing is ever written")
	}
}

func TestSession_Claude_ReadyIsImmediate(t *testing.T) {
	proc := newFakeProcess() // never writes anything, matching the real CLI's confirmed behavior
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), claudeSessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Ready(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Ready: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Ready must not wait for a line claude never sends before a turn")
	}
}

func TestSession_Turn_EncodesAgyFormat(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	go proc.writeLine(`{"event":"result","result":{"status":"SUCCESS","response":"{\"n\":1}"}}`)
	res, err := s.Turn(context.Background(), `Reply with exactly the JSON {"n":1}`)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var sent struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace([]byte(proc.stdin.String())), &sent); err != nil {
		t.Fatalf("stdin was not valid JSON: %v (%q)", err, proc.stdin.String())
	}
	if sent.Event != "user" || sent.Message.Content != `Reply with exactly the JSON {"n":1}` {
		t.Errorf("sent = %+v", sent)
	}
	var top struct {
		Event string `json:"event"`
	}
	if json.Unmarshal(res.Output, &top) != nil || top.Event != "result" {
		t.Errorf("Turn did not hand back the raw result line: %s", res.Output)
	}
}

func TestSession_Turn_EncodesClaudeFormat(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), claudeSessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	go proc.writeLine(`{"type":"result","is_error":false,"result":"{\"n\":1}"}`)
	if _, err := s.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var sent struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace([]byte(proc.stdin.String())), &sent); err != nil {
		t.Fatalf("stdin was not valid JSON: %v", err)
	}
	if sent.Type != "user" || sent.Message.Role != "user" || len(sent.Message.Content) != 1 ||
		sent.Message.Content[0].Type != "text" || sent.Message.Content[0].Text != "hi" {
		t.Errorf("sent = %+v", sent)
	}
}

func TestSession_Turn_SkipsProgressLines(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	go func() {
		proc.writeLine(`{"event":"step_update","step_update":{"state":"ACTIVE"}}`)
		proc.writeLine(`{"event":"step_update","step_update":{"state":"DONE"}}`)
		proc.writeLine(`{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`)
	}()
	res, err := s.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	var top map[string]any
	json.Unmarshal(res.Output, &top)
	if top["event"] != "result" {
		t.Errorf("Turn returned a progress line instead of the result: %s", res.Output)
	}
}

func TestSession_Turn_CalledTwiceErrSessionUsed(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	go proc.writeLine(`{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`)
	if _, err := s.Turn(context.Background(), "first"); err != nil {
		t.Fatalf("first Turn: %v", err)
	}
	if _, err := s.Turn(context.Background(), "second"); err != ErrSessionUsed {
		t.Errorf("second Turn: err = %v, want ErrSessionUsed", err)
	}
}

func TestSession_Turn_ContextDeadline(t *testing.T) {
	proc := newFakeProcess() // never writes a result
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Turn(ctx, "hi"); err == nil {
		t.Error("Turn must return once ctx is done, not hang forever")
	}
}

func TestSession_Close_WaitsThenClosesStdin(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !proc.stdin.closed {
		t.Error("Close must close Stdin")
	}
}

func TestSession_Close_KillsAStuckProcess(t *testing.T) {
	proc := newFakeProcess()
	proc.waitBlockedOn = make(chan struct{}) // never closed on its own: Wait blocks until Kill
	go func() {
		// a real process's Wait only returns once something ends it; here that is Close's own
		// Kill fallback, observed by polling the recorded reason rather than a fixed sleep, so
		// this does not race Close's exact 2s grace period.
		for {
			proc.mu.Lock()
			killed := len(proc.killed) > 0
			proc.mu.Unlock()
			if killed {
				close(proc.waitBlockedOn)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	start := time.Now()
	if _, err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if time.Since(start) < 2*time.Second {
		t.Error("Close returned before its own grace period elapsed; Kill fallback path was not exercised")
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	if len(proc.killed) != 1 || proc.killed[0] != "session closed" {
		t.Errorf("killed = %v", proc.killed)
	}
}

func TestSession_Close_Idempotent(t *testing.T) {
	proc := newFakeProcess()
	rt := &fakeSessionRuntime{proc: proc}
	s, err := (&Runner{Runtime: rt}).StartSession(context.Background(), agySessionReq(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
