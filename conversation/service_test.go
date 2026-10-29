package conversation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/run/stream"
	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/sandboxmock"
)

// ---- test doubles ----------------------------------------------------------------------

// memStore is a minimal, correct in-memory Store local to this package (memstore imports
// conversation, so it cannot be used here without an import cycle). It is deliberately simple:
// the full contract is exercised against memstore by storetest.
type memStore struct {
	mu     sync.Mutex
	convs  map[string]*Conversation
	turns  map[string]*Turn
	client map[string]string
	events map[string][]Event
	// hooks for race tests
	beforeList func()
	afterList  func()
}

func newMemStore() *memStore {
	return &memStore{convs: map[string]*Conversation{}, turns: map[string]*Turn{}, client: map[string]string{}, events: map[string][]Event{}}
}

func (s *memStore) CreateConversation(_ context.Context, c Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.convs[c.ID] = &c
	return nil
}

func (s *memStore) GetConversation(_ context.Context, id string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return *c, nil
}

func (s *memStore) ListConversations(_ context.Context, owner Principal, _ string, _ int) ([]Conversation, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Conversation
	for _, c := range s.convs {
		if c.Owner == owner {
			out = append(out, *c)
		}
	}
	return out, "", nil
}

func (s *memStore) ClaimTurn(_ context.Context, t Turn) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[t.ConversationID]
	if !ok {
		return Turn{}, ErrNotFound
	}
	if id, ok := s.client[t.ConversationID+"|"+t.ClientMessageID]; ok {
		return *s.turns[id], ErrDuplicate
	}
	if c.ActiveTurnID != "" {
		return Turn{}, ErrTurnInProgress
	}
	s.turns[t.ID] = &t
	s.client[t.ConversationID+"|"+t.ClientMessageID] = t.ID
	c.ActiveTurnID = t.ID
	return t, nil
}

func (s *memStore) GetTurn(_ context.Context, id string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.turns[id]
	if !ok {
		return Turn{}, ErrNotFound
	}
	return *t, nil
}

func (s *memStore) RenewLease(_ context.Context, id string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.turns[id]; ok {
		t.LeaseUntil = until
	}
	return nil
}

func (s *memStore) RequestCancel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.turns[id]
	if !ok {
		return ErrNotFound
	}
	if t.Status.Finished() {
		return ErrTurnNotRunning
	}
	t.CancelRequested = true
	return nil
}

func (s *memStore) CancelRequested(_ context.Context, ids []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range ids {
		if t, ok := s.turns[id]; ok && t.CancelRequested && !t.Status.Finished() {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *memStore) FinishTurn(_ context.Context, t Turn, sessionID string, resumable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.turns[t.ID]
	cancel := st.CancelRequested
	*st = t
	st.CancelRequested = cancel
	c := s.convs[t.ConversationID]
	c.ActiveTurnID, c.Resumable = "", resumable
	if sessionID != "" {
		c.SessionID = sessionID
	}
	return nil
}

func (s *memStore) StaleTurns(_ context.Context, now time.Time) ([]Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Turn
	for _, t := range s.turns {
		if !t.Status.Finished() && t.LeaseUntil.Before(now) {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (s *memStore) AppendEvents(_ context.Context, id string, evs []Event) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(evs))
	for i, e := range evs {
		e.ConversationID, e.Seq = id, int64(len(s.events[id])+1)
		s.events[id] = append(s.events[id], e)
		out[i] = e
	}
	return out, nil
}

func (s *memStore) ListEvents(_ context.Context, id string, after int64, limit int) ([]Event, error) {
	if s.beforeList != nil {
		s.beforeList()
	}
	s.mu.Lock()
	log := s.events[id]
	var out []Event
	if after < int64(len(log)) {
		out = append(out, log[after:]...)
		if limit > 0 && len(out) > limit {
			out = out[:limit]
		}
	}
	s.mu.Unlock()
	if s.afterList != nil {
		s.afterList()
	}
	return out, nil
}

type resolver struct {
	mu        sync.Mutex
	workspace string
	extraArgs []string
	err       error
	saveErr   error
	saved     []map[string]string
	resolved  int
}

func (r *resolver) Resolve(context.Context, Principal, Conversation) (run.Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved++
	if r.err != nil {
		return run.Request{}, r.err
	}
	// A hostile or careless resolver: the service must overwrite all of these.
	return run.Request{Workspace: r.workspace, Profile: "claude_code", Command: "rm", Args: []string{"-rf"}, ExtraArgs: r.extraArgs, Prompt: "ignored"}, nil
}

func (r *resolver) SaveCredentials(_ context.Context, _ Principal, _ Conversation, files map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, files)
	return r.saveErr
}

// blockingRuntime starts a run, writes a first line, then waits to be released or cancelled.
type blockingRuntime struct {
	sandbox.StubRuntime
	started chan struct{}
	release chan struct{}
	stdout  string
}

func newBlocking() *blockingRuntime {
	return &blockingRuntime{started: make(chan struct{}, 16), release: make(chan struct{}), stdout: agyLine("init", "")}
}

func (b *blockingRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	if req.Output != nil {
		_, _ = io.WriteString(req.Output, b.stdout)
	}
	b.started <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return &sandbox.CommandResult{Stdout: b.stdout}, nil
	}
}

func agyLine(kind, id string) string {
	if kind == "init" {
		return `{"event":"init","conversation_id":"blocking-1","init":{}}` + "\n"
	}
	return ""
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "run", "stream", "agy", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type harness struct {
	t   *testing.T
	svc *Service
	st  *memStore
	res *resolver
	rt  sandbox.Runtime
}

type opt func(*Config)

func newHarness(t *testing.T, rt sandbox.Runtime, opts ...opt) *harness {
	t.Helper()
	h := &harness{t: t, st: newMemStore(), res: &resolver{workspace: t.TempDir()}, rt: rt}
	cfg := Config{
		Runner: &run.Runner{Runtime: rt}, Store: h.st, Authorizer: OwnerOnly{}, Resolver: h.res,
		Profiles: []string{"antigravity"}, LeaseDuration: time.Minute, CancelPoll: 10 * time.Millisecond, EventPoll: 20 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	return h
}

func (h *harness) create(p Principal) Conversation {
	h.t.Helper()
	c, err := h.svc.Create(context.Background(), p, "antigravity", "t")
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) send(p Principal, id, msg, cid string) Turn {
	h.t.Helper()
	tr, dup, err := h.svc.Send(context.Background(), p, id, msg, cid)
	if err != nil || dup {
		h.t.Fatalf("Send: dup=%v err=%v", dup, err)
	}
	return tr
}

// settle waits until the conversation has no running turn.
func (h *harness) settle(id string) Conversation {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := h.st.GetConversation(context.Background(), id)
		if err != nil {
			h.t.Fatal(err)
		}
		if c.ActiveTurnID == "" {
			return c
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("turn still running after 5s: %+v", c)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) history(id string) []Event {
	h.t.Helper()
	evs, err := h.st.ListEvents(context.Background(), id, 0, 10000)
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

func kinds(evs []Event) []stream.Kind {
	out := make([]stream.Kind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func text(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == stream.KindAssistantText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func lastEvent(evs []Event) Event { return evs[len(evs)-1] }

const alice, bob = Principal("alice"), Principal("bob")

func scripted(results ...*sandbox.CommandResult) *sandboxmock.Runtime {
	return &sandboxmock.Runtime{Results: results}
}

func out(t *testing.T, name string) *sandbox.CommandResult {
	return &sandbox.CommandResult{Stdout: fixture(t, name)}
}

// ---- tests -----------------------------------------------------------------------------

func TestNewService_Validation(t *testing.T) {
	base := Config{Runner: &run.Runner{Runtime: scripted()}, Store: newMemStore(), Resolver: &resolver{}, Profiles: []string{"antigravity"}}
	tests := []struct {
		name   string
		mutate func(*Config)
		ok     bool
	}{
		{"valid", func(*Config) {}, true},
		{"no runner", func(c *Config) { c.Runner = nil }, false},
		{"no store", func(c *Config) { c.Store = nil }, false},
		{"no resolver", func(c *Config) { c.Resolver = nil }, false},
		{"no profiles", func(c *Config) { c.Profiles = nil }, false},
		{"profile that cannot stream", func(c *Config) { c.Profiles = []string{"claude_code"} }, false},
		{"unknown profile", func(c *Config) { c.Profiles = []string{"nope"} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			_, err := NewService(cfg)
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	h := newHarness(t, scripted())
	ctx := context.Background()
	tests := []struct {
		name    string
		p       Principal
		profile string
		title   string
		want    error
	}{
		{"ok", alice, "antigravity", "hello", nil},
		{"empty principal denied", "", "antigravity", "", ErrForbidden},
		{"profile not on the allowlist", alice, "antigravity_notools", "", ErrProfileNotAllowed},
		{"unknown profile", alice, "../../bin/sh", "", ErrProfileNotAllowed},
		{"title too long", alice, "antigravity", strings.Repeat("é", 256), ErrInvalidArgument},
		{"title at the limit", alice, "antigravity", strings.Repeat("é", 255), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := h.svc.Create(ctx, tt.p, tt.profile, tt.title)
			if !errors.Is(err, tt.want) && (tt.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (c.ID == "" || len(c.ID) < 22 || c.Owner != tt.p || !c.Resumable || c.SessionID != "") {
				t.Errorf("conversation = %+v", c)
			}
		})
	}
}

func TestDefaultAuthorizerDeniesEverything(t *testing.T) {
	h := newHarness(t, scripted(), func(c *Config) { c.Authorizer = nil })
	if _, err := h.svc.Create(context.Background(), alice, "antigravity", ""); !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestSendRunsATurnAndRecordsHistory(t *testing.T) {
	h := newHarness(t, scripted(out(t, "agy-1.4.8-tools.jsonl")))
	c := h.create(alice)
	h.res.extraArgs = []string{"--injected"}
	tr := h.send(alice, c.ID, "read the note", "m1")
	if tr.Status != TurnRunning || tr.ID == "" {
		t.Errorf("turn = %+v", tr)
	}
	got := h.settle(c.ID)

	evs := h.history(c.ID)
	want := []stream.Kind{stream.KindUserMessage, stream.KindTurnStarted, stream.KindToolUse, stream.KindToolResult, stream.KindAssistantText, stream.KindTurnCompleted}
	if k := kinds(evs); len(k) != len(want) {
		t.Fatalf("kinds = %v, want %v", k, want)
	}
	for i, e := range evs {
		if e.Kind != want[i] || e.Seq != int64(i+1) || e.TurnID != tr.ID {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	if evs[0].Text != "read the note" || !strings.Contains(text(evs), "secret-marker") {
		t.Errorf("texts: user=%q assistant=%q", evs[0].Text, text(evs))
	}
	if last := lastEvent(evs); last.Meta["total_tokens"] == "" || last.Meta["failure"] != "" {
		t.Errorf("final event meta = %v", last.Meta)
	}
	if got.SessionID != "22222222-2222-4222-8222-222222222222" || !got.Resumable {
		t.Errorf("conversation = %+v", got)
	}
	stored, _ := h.st.GetTurn(context.Background(), tr.ID)
	if stored.Status != TurnCompleted || stored.FinishedAt.IsZero() {
		t.Errorf("stored turn = %+v", stored)
	}

	// Nothing the resolver (or a client) set may change how the CLI is run.
	req := h.rt.(*sandboxmock.Runtime).Requests()[0]
	script := req.Command[2]
	if strings.Contains(script, "injected") || strings.Contains(script, "'rm'") || !strings.Contains(script, "'agy'") || !strings.Contains(script, "'-p' 'Read and follow") {
		t.Errorf("script was influenced by the resolver:\n%s", script)
	}
}

func TestSecondTurnResumesTheCLISession(t *testing.T) {
	rt := scripted(out(t, "agy-1.4.8-basic.jsonl"), out(t, "agy-1.4.8-resume.jsonl"))
	h := newHarness(t, rt)
	c := h.create(alice)
	h.send(alice, c.ID, "reply with pong", "m1")
	h.settle(c.ID)
	h.send(alice, c.ID, "what did I ask?", "m2")
	got := h.settle(c.ID)

	reqs := rt.Requests()
	if len(reqs) != 2 {
		t.Fatalf("runs = %d", len(reqs))
	}
	if strings.Contains(reqs[0].Command[2], "--conversation") {
		t.Error("the first turn must not resume anything")
	}
	if !strings.Contains(reqs[1].Command[2], "'--conversation' '11111111-1111-4111-8111-111111111111'") {
		t.Errorf("second turn does not resume:\n%s", reqs[1].Command[2])
	}
	if !got.Resumable || got.SessionID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("conversation = %+v", got)
	}
	var texts int
	for _, e := range h.history(c.ID) {
		if e.Kind == stream.KindUserMessage {
			texts++
		}
	}
	if texts != 2 {
		t.Errorf("user messages in history = %d, want 2", texts)
	}
}

func TestSendIsIdempotentPerClientMessageID(t *testing.T) {
	rt := scripted(out(t, "agy-1.4.8-basic.jsonl"))
	h := newHarness(t, rt)
	c := h.create(alice)
	first := h.send(alice, c.ID, "hello", "same-id")
	again, dup, err := h.svc.Send(context.Background(), alice, c.ID, "hello", "same-id")
	if err != nil || !dup || again.ID != first.ID {
		t.Fatalf("retry = %+v, dup=%v, err=%v", again, dup, err)
	}
	h.settle(c.ID)
	again, dup, err = h.svc.Send(context.Background(), alice, c.ID, "hello", "same-id")
	if err != nil || !dup || again.ID != first.ID {
		t.Fatalf("retry after finish = %+v, dup=%v, err=%v", again, dup, err)
	}
	if n := len(rt.Requests()); n != 1 {
		t.Errorf("CLI runs = %d, want 1", n)
	}
}

func TestSendValidationAndAccess(t *testing.T) {
	h := newHarness(t, scripted(), func(c *Config) { c.MaxMessageBytes = 10 })
	c := h.create(alice)
	ctx := context.Background()
	tests := []struct {
		name string
		p    Principal
		id   string
		msg  string
		cid  string
		want error
	}{
		{"empty message", alice, c.ID, "", "m1", ErrInvalidArgument},
		{"message too large", alice, c.ID, strings.Repeat("a", 11), "m1", ErrMessageTooLarge},
		{"invalid utf-8", alice, c.ID, "\xff\xfe", "m1", ErrInvalidArgument},
		{"empty client id", alice, c.ID, "hi", "", ErrInvalidArgument},
		{"client id with a slash", alice, c.ID, "hi", "a/b", ErrInvalidArgument},
		{"client id too long", alice, c.ID, "hi", strings.Repeat("a", 65), ErrInvalidArgument},
		{"someone else's conversation looks like it does not exist", bob, c.ID, "hi", "m1", ErrNotFound},
		{"unknown conversation", alice, "nope", "hi", "m1", ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := h.svc.Send(ctx, tt.p, tt.id, tt.msg, tt.cid)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if n := len(h.history(c.ID)); n != 0 {
		t.Errorf("rejected sends left %d events", n)
	}
}

func TestOnlyOneTurnRunsAtATime(t *testing.T) {
	rt := newBlocking()
	h := newHarness(t, rt)
	c := h.create(alice)
	first := h.send(alice, c.ID, "one", "m1")
	<-rt.started
	if _, _, err := h.svc.Send(context.Background(), alice, c.ID, "two", "m2"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("err = %v, want ErrTurnInProgress", err)
	}
	close(rt.release)
	h.settle(c.ID)
	if stored, _ := h.st.GetTurn(context.Background(), first.ID); stored.Status != TurnCompleted {
		t.Errorf("first turn = %+v", stored)
	}
}

func TestActiveTurnLimitPerPrincipal(t *testing.T) {
	rt := newBlocking()
	h := newHarness(t, rt, func(c *Config) { c.MaxActiveTurns = 1 })
	c1, c2 := h.create(alice), h.create(alice)
	h.send(alice, c1.ID, "one", "m1")
	<-rt.started
	if _, _, err := h.svc.Send(context.Background(), alice, c2.ID, "two", "m1"); !errors.Is(err, ErrTooManyTurns) {
		t.Fatalf("err = %v, want ErrTooManyTurns", err)
	}
	close(rt.release)
	h.settle(c1.ID)
	// The slot is free again.
	if _, _, err := h.svc.Send(context.Background(), alice, c2.ID, "two", "m1"); err != nil {
		t.Errorf("after the first turn ended: %v", err)
	}
	h.settle(c2.ID)
}

func TestExpiredCLISessionMakesConversationNonResumableButKeepsHistory(t *testing.T) {
	// Turn 2 resumes session 1111…, but agy answers from a brand-new conversation (3333…).
	rt := scripted(out(t, "agy-1.4.8-basic.jsonl"), out(t, "agy-1.4.8-notfound.jsonl"))
	h := newHarness(t, rt)
	c := h.create(alice)
	h.send(alice, c.ID, "one", "m1")
	h.settle(c.ID)
	tr := h.send(alice, c.ID, "two", "m2")
	got := h.settle(c.ID)

	if got.Resumable {
		t.Error("conversation must not be resumable any more")
	}
	if got.SessionID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("the unwanted new session ID was kept: %q", got.SessionID)
	}
	evs := h.history(c.ID)
	last := lastEvent(evs)
	if last.Kind != stream.KindTurnFailed || last.Meta["failure"] != string(FailSessionExpired) || last.TurnID != tr.ID {
		t.Errorf("final event = %+v", last)
	}
	if strings.Contains(text(evs[lenUntilTurn(evs, tr.ID):]), "Hello! How can I help you today?") {
		t.Error("the answer of the accidental new conversation was shown")
	}
	if _, _, err := h.svc.Send(context.Background(), alice, c.ID, "three", "m3"); !errors.Is(err, ErrNotResumable) {
		t.Errorf("err = %v, want ErrNotResumable", err)
	}
	// History and export still work.
	if _, err := h.svc.Events(context.Background(), alice, c.ID, 0, 100); err != nil {
		t.Errorf("Events: %v", err)
	}
	if _, err := h.svc.Export(context.Background(), alice, c.ID, FormatMarkdown, ""); err != nil {
		t.Errorf("Export: %v", err)
	}
}

func lenUntilTurn(evs []Event, turnID string) int {
	for i, e := range evs {
		if e.TurnID == turnID {
			return i
		}
	}
	return len(evs)
}

func TestFailureMapping(t *testing.T) {
	tests := []struct {
		name       string
		result     *sandbox.CommandResult
		want       FailureCode
		resumable  bool
		retryAfter time.Duration
	}{
		{"quota", &sandbox.CommandResult{ExitCode: 1, Stderr: `{"error":"Individual quota reached. Resets in 2h0m0s."}`}, FailQuotaExceeded, true, 2 * time.Hour},
		{"plain failure", &sandbox.CommandResult{ExitCode: 3, Stderr: "something broke"}, FailRunFailed, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, scripted(tt.result))
			c := h.create(alice)
			h.send(alice, c.ID, "go", "m1")
			got := h.settle(c.ID)
			last := lastEvent(h.history(c.ID))
			if last.Kind != stream.KindTurnFailed || last.Meta["failure"] != string(tt.want) {
				t.Fatalf("final event = %+v", last)
			}
			if got.Resumable != tt.resumable {
				t.Errorf("resumable = %v", got.Resumable)
			}
			if tt.retryAfter > 0 && last.Meta["retry_after_seconds"] != "7200" {
				t.Errorf("retry_after_seconds = %q", last.Meta["retry_after_seconds"])
			}
		})
	}
}

func TestResolverErrorFailsTheTurnWithoutLeakingDetails(t *testing.T) {
	h := newHarness(t, scripted())
	h.res.err = errors.New("database password is hunter2")
	c := h.create(alice)
	h.send(alice, c.ID, "go", "m1")
	h.settle(c.ID)
	evs := h.history(c.ID)
	last := lastEvent(evs)
	if last.Meta["failure"] != string(FailRunFailed) {
		t.Fatalf("final event = %+v", last)
	}
	for _, e := range evs {
		if strings.Contains(e.Text+e.Meta["failure"], "hunter2") {
			t.Error("resolver error text reached the history")
		}
	}
}

func TestRefreshedCredentialsAreHandedBackToTheHost(t *testing.T) {
	tests := []struct {
		name     string
		saveErr  error
		wantFlag bool
	}{
		{"saved", nil, false},
		{"save fails", errors.New("disk full"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := out(t, "agy-1.4.8-basic.jsonl")
			res.UpdatedCredentialFiles = map[string]string{"/home/agent/.gemini/creds.json": "NEW-TOKEN"}
			h := newHarness(t, scripted(res))
			h.res.saveErr = tt.saveErr
			c := h.create(alice)
			tr := h.send(alice, c.ID, "go", "m1")
			h.settle(c.ID)

			if len(h.res.saved) != 1 || h.res.saved[0][".gemini/creds.json"] != "NEW-TOKEN" {
				t.Fatalf("saved = %v", h.res.saved)
			}
			stored, _ := h.st.GetTurn(context.Background(), tr.ID)
			if stored.CredentialsNotSaved != tt.wantFlag || stored.Status != TurnCompleted {
				t.Errorf("turn = %+v", stored)
			}
			if got := lastEvent(h.history(c.ID)).Meta["credentials_not_saved"] == "true"; got != tt.wantFlag {
				t.Errorf("event flag = %v", got)
			}
			for _, e := range h.history(c.ID) {
				if strings.Contains(e.Text, "NEW-TOKEN") {
					t.Error("credential content reached the history")
				}
			}
		})
	}
}

func TestNoCredentialSaveWhenNothingRefreshed(t *testing.T) {
	h := newHarness(t, scripted(out(t, "agy-1.4.8-basic.jsonl")))
	c := h.create(alice)
	h.send(alice, c.ID, "go", "m1")
	h.settle(c.ID)
	if len(h.res.saved) != 0 {
		t.Errorf("SaveCredentials called with %v", h.res.saved)
	}
}

func TestCancel(t *testing.T) {
	rt := newBlocking()
	h := newHarness(t, rt)
	c := h.create(alice)
	tr := h.send(alice, c.ID, "go", "m1")
	<-rt.started

	if err := h.svc.Cancel(context.Background(), bob, c.ID, tr.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else cancelling: %v, want ErrNotFound", err)
	}
	if err := h.svc.Cancel(context.Background(), alice, c.ID, tr.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := h.svc.Cancel(context.Background(), alice, c.ID, tr.ID); err != nil && !errors.Is(err, ErrTurnNotRunning) {
		t.Errorf("second Cancel: %v", err)
	}
	got := h.settle(c.ID)
	stored, _ := h.st.GetTurn(context.Background(), tr.ID)
	if stored.Status != TurnCancelled || !got.Resumable {
		t.Errorf("turn = %+v, conversation = %+v", stored, got)
	}
	if last := lastEvent(h.history(c.ID)); last.Kind != stream.KindTurnCancelled {
		t.Errorf("final event = %+v", last)
	}
	if err := h.svc.Cancel(context.Background(), alice, c.ID, tr.ID); !errors.Is(err, ErrTurnNotRunning) {
		t.Errorf("cancel after finish: %v, want ErrTurnNotRunning", err)
	}
	if err := h.svc.Cancel(context.Background(), alice, c.ID, "no-such-turn"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown turn: %v", err)
	}
}

func TestCancelFromAnotherInstance(t *testing.T) {
	rt := newBlocking()
	a := newHarness(t, rt)
	// Instance B shares the store and authorizer but runs nothing.
	b, err := NewService(Config{
		Runner: &run.Runner{Runtime: rt}, Store: a.st, Authorizer: OwnerOnly{}, Resolver: a.res,
		Profiles: []string{"antigravity"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())

	c := a.create(alice)
	tr := a.send(alice, c.ID, "go", "m1")
	<-rt.started

	if err := b.Cancel(context.Background(), alice, c.ID, tr.ID); err != nil {
		t.Fatalf("Cancel on B: %v", err)
	}
	a.settle(c.ID) // instance A notices within CancelPoll
	if stored, _ := a.st.GetTurn(context.Background(), tr.ID); stored.Status != TurnCancelled {
		t.Errorf("turn = %+v", stored)
	}
}

func TestCloseInterruptsRunningTurns(t *testing.T) {
	rt := newBlocking()
	h := newHarness(t, rt)
	c := h.create(alice)
	tr := h.send(alice, c.ID, "go", "m1")
	<-rt.started

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stored, _ := h.st.GetTurn(context.Background(), tr.ID)
	if stored.Status != TurnFailed || stored.Failure != FailInterrupted {
		t.Errorf("turn = %+v", stored)
	}
	if got, _ := h.st.GetConversation(context.Background(), c.ID); got.ActiveTurnID != "" || !got.Resumable {
		t.Errorf("conversation = %+v", got)
	}
}

func TestRecoverStale(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, scripted(), func(c *Config) { c.Now = func() time.Time { return now } })
	c := h.create(alice)
	ctx := context.Background()
	// A turn whose owner died: running, lease long expired, no live process here.
	if _, err := h.st.ClaimTurn(ctx, Turn{ID: "dead", ConversationID: c.ID, ClientMessageID: "m", Status: TurnRunning, LeaseUntil: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	n, err := h.svc.RecoverStale(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RecoverStale = %d, %v", n, err)
	}
	stored, _ := h.st.GetTurn(ctx, "dead")
	if stored.Status != TurnFailed || stored.Failure != FailInterrupted {
		t.Errorf("turn = %+v", stored)
	}
	if got, _ := h.st.GetConversation(ctx, c.ID); got.ActiveTurnID != "" || !got.Resumable {
		t.Errorf("conversation = %+v", got)
	}
	if last := lastEvent(h.history(c.ID)); last.Kind != stream.KindTurnFailed || last.Meta["failure"] != string(FailInterrupted) {
		t.Errorf("final event = %+v", last)
	}
	if n, _ := h.svc.RecoverStale(ctx); n != 0 {
		t.Errorf("second RecoverStale = %d", n)
	}
}

func TestReadsAreAuthorizedAndValidated(t *testing.T) {
	h := newHarness(t, scripted())
	c := h.create(alice)
	ctx := context.Background()
	if _, err := h.svc.Get(ctx, bob, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get as bob: %v", err)
	}
	if _, err := h.svc.Events(ctx, bob, c.ID, 0, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("Events as bob: %v", err)
	}
	if _, err := h.svc.Export(ctx, bob, c.ID, FormatJSON, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("Export as bob: %v", err)
	}
	if _, err := h.svc.Subscribe(ctx, bob, c.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Subscribe as bob: %v", err)
	}
	for _, tt := range []struct {
		after int64
		limit int
	}{{-1, 10}, {0, 0}, {0, 501}} {
		if _, err := h.svc.Events(ctx, alice, c.ID, tt.after, tt.limit); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Events(%d, %d): %v", tt.after, tt.limit, err)
		}
	}
	for _, limit := range []int{0, 101, -3} {
		if _, _, err := h.svc.List(ctx, alice, "", limit); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("List(limit=%d): %v", limit, err)
		}
	}
	if _, err := h.svc.Export(ctx, alice, c.ID, "pdf", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Export pdf: %v", err)
	}
	list, _, err := h.svc.List(ctx, alice, "", 10)
	if err != nil || len(list) != 1 {
		t.Errorf("List = %v, %v", list, err)
	}
}

func TestExport(t *testing.T) {
	h := newHarness(t, scripted(out(t, "agy-1.4.8-tools.jsonl")))
	c := h.create(alice)
	tr := h.send(alice, c.ID, "read the note", "m1")
	h.settle(c.ID)
	ctx := context.Background()

	md, err := h.svc.Export(ctx, alice, c.ID, FormatMarkdown, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# t", "## You", "read the note", "## Assistant", "secret-marker", "tool `read_file`"} {
		if !strings.Contains(string(md.Data), want) {
			t.Errorf("markdown lacks %q:\n%s", want, md.Data)
		}
	}
	if !strings.HasPrefix(md.ContentType, "text/markdown") || !strings.HasSuffix(md.Filename, ".md") {
		t.Errorf("md meta = %q %q", md.ContentType, md.Filename)
	}

	js, err := h.svc.Export(ctx, alice, c.ID, FormatJSON, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"22222222-2222-4222-8222-222222222222", "alice", "SessionID", "owner"} {
		if strings.Contains(string(js.Data), leak) {
			t.Errorf("JSON export leaks %q:\n%s", leak, js.Data)
		}
	}
	if js.ContentType != "application/json" || !strings.Contains(string(js.Data), `"kind": "turn.completed"`) {
		t.Errorf("json export = %q\n%s", js.ContentType, js.Data)
	}

	other, err := h.svc.Export(ctx, alice, c.ID, FormatJSON, "another-turn")
	if err != nil || strings.Contains(string(other.Data), "secret-marker") {
		t.Errorf("turn filter: %v\n%s", err, other.Data)
	}
}

// ---- subscribe -------------------------------------------------------------------------

func collectN(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d of %d events", len(got), n)
			}
			got = append(got, e)
		case <-timeout:
			t.Fatalf("got %d of %d events", len(got), n)
		}
	}
	return got
}

func assertSeqs(t *testing.T, evs []Event, from, to int64) {
	t.Helper()
	if int64(len(evs)) != to-from+1 {
		t.Fatalf("got %d events, want seq %d..%d", len(evs), from, to)
	}
	for i, e := range evs {
		if e.Seq != from+int64(i) {
			t.Fatalf("event %d has seq %d, want %d (gap or duplicate)", i, e.Seq, from+int64(i))
		}
	}
}

// appendAndPublish does what a turn's writer does: store, then fan out.
func (h *harness) appendAndPublish(id string, n int) {
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = Event{Kind: stream.KindAssistantText, Text: "x"}
	}
	stored, err := h.st.AppendEvents(context.Background(), id, evs)
	if err != nil {
		h.t.Error(err)
		return
	}
	h.svc.broker.publish(id, stored)
}

func TestSubscribeReplaysHistoryThenGoesLive(t *testing.T) {
	h := newHarness(t, scripted())
	c := h.create(alice)
	h.appendAndPublish(c.ID, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := h.svc.Subscribe(ctx, alice, c.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertSeqs(t, collectN(t, ch, 3), 3, 5)
	h.appendAndPublish(c.ID, 4)
	assertSeqs(t, collectN(t, ch, 4), 6, 9)
}

func TestSubscribeHandoverRace(t *testing.T) {
	// Events appear in the store and on the broker both just before and just after the
	// replay reads the store. Every event must arrive exactly once, in order.
	for i := 0; i < 200; i++ {
		h := newHarness(t, scripted())
		c := h.create(alice)
		h.appendAndPublish(c.ID, 3) // history

		var before, after sync.Once
		// Lands in the store before the replay reads it AND sits in the broker queue: the
		// subscriber sees these twice unless it deduplicates by Seq.
		h.st.beforeList = func() { before.Do(func() { h.appendAndPublish(c.ID, 2) }) }
		// Lands after the replay read the store: only the broker (or a later catch-up) has it.
		h.st.afterList = func() { after.Do(func() { h.appendAndPublish(c.ID, 2) }) }

		ctx, cancel := context.WithCancel(context.Background())
		ch, err := h.svc.Subscribe(ctx, alice, c.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		assertSeqs(t, collectN(t, ch, 7), 1, 7)
		cancel()
	}
}

func TestSlowSubscriberStillGetsEveryEvent(t *testing.T) {
	h := newHarness(t, scripted(), func(c *Config) { c.EventPoll = time.Hour })
	c := h.create(alice)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := h.svc.Subscribe(ctx, alice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Far more than the broker buffer plus the output channel, published without reading.
	const total = 2000
	for i := 0; i < total/10; i++ {
		h.appendAndPublish(c.ID, 10)
	}
	assertSeqs(t, collectN(t, ch, total), 1, total)
}

func TestSubscriberSeesTurnsRunningOnAnotherInstance(t *testing.T) {
	// Nothing is published on this instance's broker; events only reach the store.
	h := newHarness(t, scripted())
	c := h.create(alice)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := h.svc.Subscribe(ctx, alice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendEvents(ctx, c.ID, []Event{{Kind: stream.KindAssistantText}, {Kind: stream.KindAssistantText}}); err != nil {
		t.Fatal(err)
	}
	assertSeqs(t, collectN(t, ch, 2), 1, 2)
}

type revocable struct {
	mu      sync.Mutex
	revoked bool
}

func (r *revocable) Authorize(ctx context.Context, p Principal, a Action, c *Conversation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked {
		return ErrForbidden
	}
	return OwnerOnly{}.Authorize(ctx, p, a, c)
}

func TestSubscriptionEndsWhenAccessIsRevoked(t *testing.T) {
	authz := &revocable{}
	h := newHarness(t, scripted(), func(c *Config) { c.Authorizer = authz; c.ReauthInterval = 10 * time.Millisecond })
	c := h.create(alice)
	ch, err := h.svc.Subscribe(context.Background(), alice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	authz.mu.Lock()
	authz.revoked = true
	authz.mu.Unlock()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("received an event after revocation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscription was not closed after access was revoked")
	}
}

func TestSubscribeUnsubscribesWhenContextEnds(t *testing.T) {
	h := newHarness(t, scripted())
	c := h.create(alice)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := h.svc.Subscribe(ctx, alice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.svc.broker.count(c.ID) != 1 {
		t.Fatal("subscriber not registered")
	}
	cancel()
	for range ch { // drained until closed
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.svc.broker.count(c.ID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscriber leaked")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLiveTurnStreamsToASubscriber(t *testing.T) {
	h := newHarness(t, scripted(out(t, "agy-1.4.8-basic.jsonl")))
	c := h.create(alice)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := h.svc.Subscribe(ctx, alice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.send(alice, c.ID, "go", "m1")
	var got []Event
	for len(got) == 0 || lastEvent(got).Kind != stream.KindTurnCompleted {
		got = append(got, collectN(t, ch, 1)...)
	}
	assertSeqs(t, got, 1, int64(len(got)))
	if got[0].Kind != stream.KindUserMessage || text(got) != "pong\n" {
		t.Errorf("events = %v, text %q", kinds(got), text(got))
	}
}
