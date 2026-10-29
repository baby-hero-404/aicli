package conversation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/run/stream"
)

// Config configures a Service.
type Config struct {
	// Runner executes the CLI. Required.
	Runner *run.Runner
	// Store, Resolver are required; Authorizer defaults to DenyAll.
	Store      Store
	Authorizer Authorizer
	Resolver   Resolver
	// Profiles is the allowlist of profile names a client may pick. Each must support
	// streaming (see profile.StreamingArgs). Required.
	Profiles []string
	// Logger receives audit and error records. It never receives message text, CLI output or
	// credentials. Defaults to a logger that discards everything.
	Logger *slog.Logger

	// LeaseDuration is how long a running turn is owned without renewal (default 2m); it is
	// renewed four times per lease. Turns whose lease expired are recovered by RecoverStale.
	LeaseDuration time.Duration
	// CancelPoll is how often a running turn checks whether another instance asked to cancel
	// it (default 2s).
	CancelPoll time.Duration
	// EventPoll is how often a subscriber re-reads the store, which is how it sees turns that
	// run on other instances (default 1s).
	EventPoll time.Duration
	// ReauthInterval is how often an open subscription re-checks authorization (default 60s).
	ReauthInterval time.Duration

	// MaxMessageBytes (default 32 KiB), MaxTitleRunes (default 255), MaxActiveTurns per
	// principal on this instance (default 2) and MaxExportEvents (default 100000).
	MaxMessageBytes int
	MaxTitleRunes   int
	MaxActiveTurns  int
	MaxExportEvents int

	// Now and NewID are for tests. NewID must return unpredictable IDs of at least 128 bits.
	Now   func() time.Time
	NewID func() (string, error)
}

// Service runs conversations. Create one with NewService; it is safe for concurrent use.
type Service struct {
	cfg    Config
	log    *slog.Logger
	broker *broker

	baseCtx context.Context
	stop    context.CancelCauseFunc
	wg      sync.WaitGroup

	mu        sync.Mutex
	running   map[string]context.CancelCauseFunc // turn ID -> cancel
	perOwner  map[Principal]int
	allowlist map[string]struct{}
}

var (
	errUserCancel = errors.New("conversation: cancelled by user")
	errShutdown   = errors.New("conversation: service shutting down")

	clientMessageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// NewService validates cfg and returns a Service.
func NewService(cfg Config) (*Service, error) {
	switch {
	case cfg.Runner == nil:
		return nil, fmt.Errorf("conversation: Runner is required")
	case cfg.Store == nil:
		return nil, fmt.Errorf("conversation: Store is required")
	case cfg.Resolver == nil:
		return nil, fmt.Errorf("conversation: Resolver is required")
	case len(cfg.Profiles) == 0:
		return nil, fmt.Errorf("conversation: at least one profile must be allowed")
	}
	allow := make(map[string]struct{}, len(cfg.Profiles))
	for _, name := range cfg.Profiles {
		p, ok := profile.Lookup(name)
		if !ok || len(p.StreamArgs) == 0 {
			return nil, fmt.Errorf("conversation: profile %q does not exist or cannot stream", name)
		}
		allow[name] = struct{}{}
	}
	if cfg.Authorizer == nil {
		cfg.Authorizer = DenyAll{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	defaultDur(&cfg.LeaseDuration, 2*time.Minute)
	defaultDur(&cfg.CancelPoll, 2*time.Second)
	defaultDur(&cfg.EventPoll, time.Second)
	defaultDur(&cfg.ReauthInterval, time.Minute)
	defaultInt(&cfg.MaxMessageBytes, 32<<10)
	defaultInt(&cfg.MaxTitleRunes, 255)
	defaultInt(&cfg.MaxActiveTurns, 2)
	defaultInt(&cfg.MaxExportEvents, 100000)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	ctx, stop := context.WithCancelCause(context.Background())
	return &Service{
		cfg: cfg, log: cfg.Logger, broker: newBroker(),
		baseCtx: ctx, stop: stop,
		running: map[string]context.CancelCauseFunc{}, perOwner: map[Principal]int{}, allowlist: allow,
	}, nil
}

func defaultDur(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

func defaultInt(n *int, v int) {
	if *n <= 0 {
		*n = v
	}
}

// randomID returns "c" followed by 128 random bits, URL-safe. The prefix guarantees a letter
// first, which sandbox.SessionDir requires of a directory name: hosts name the per-conversation
// session directory after the ID. An unusable randomness source is an error: a predictable ID is
// never an acceptable fallback.
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("conversation: generate id: %w", err)
	}
	return "c" + base64.RawURLEncoding.EncodeToString(b), nil
}

// Close stops accepting turns' work: every running turn is interrupted and recorded as such,
// and Close waits for them to finish or for ctx to end.
func (s *Service) Close(ctx context.Context) error {
	s.stop(errShutdown)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("conversation: close: %w", ctx.Err())
	}
}

// authorize asks the Authorizer. A denial on an existing conversation is reported as
// ErrNotFound so that its existence is not revealed.
func (s *Service) authorize(ctx context.Context, p Principal, a Action, c *Conversation) error {
	err := s.cfg.Authorizer.Authorize(ctx, p, a, c)
	if err == nil {
		return nil
	}
	convID := ""
	if c != nil {
		convID = c.ID
	}
	s.audit(p, a, convID, "", "denied")
	if c != nil {
		return ErrNotFound
	}
	return ErrForbidden
}

// load fetches a conversation and authorizes the action on it.
func (s *Service) load(ctx context.Context, p Principal, id string, a Action) (Conversation, error) {
	c, err := s.cfg.Store.GetConversation(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.audit(p, a, id, "", "not_found")
			return Conversation{}, ErrNotFound
		}
		return Conversation{}, fmt.Errorf("conversation: load: %w", err)
	}
	if err := s.authorize(ctx, p, a, &c); err != nil {
		return Conversation{}, err
	}
	return c, nil
}

func (s *Service) audit(p Principal, a Action, convID, turnID, outcome string) {
	s.log.Info("conversation", "action", string(a), "principal", string(p),
		"conversation_id", convID, "turn_id", turnID, "outcome", outcome)
}

// Create starts a new, empty conversation.
func (s *Service) Create(ctx context.Context, p Principal, profileName, title string) (Conversation, error) {
	if err := s.authorize(ctx, p, ActionCreate, nil); err != nil {
		return Conversation{}, err
	}
	if _, ok := s.allowlist[profileName]; !ok {
		return Conversation{}, fmt.Errorf("%w: %q", ErrProfileNotAllowed, profileName)
	}
	if utf8.RuneCountInString(title) > s.cfg.MaxTitleRunes {
		return Conversation{}, fmt.Errorf("%w: title is longer than %d characters", ErrInvalidArgument, s.cfg.MaxTitleRunes)
	}
	id, err := s.cfg.NewID()
	if err != nil {
		return Conversation{}, err
	}
	now := s.cfg.Now()
	c := Conversation{
		ID: id, Owner: p, Profile: profileName, Title: title,
		Resumable: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.cfg.Store.CreateConversation(ctx, c); err != nil {
		return Conversation{}, fmt.Errorf("conversation: create: %w", err)
	}
	s.audit(p, ActionCreate, c.ID, "", "ok")
	return c, nil
}

// Get returns a conversation the principal may read.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Conversation, error) {
	return s.load(ctx, p, id, ActionRead)
}

// List returns the principal's conversations, newest first, and the cursor for the next page.
func (s *Service) List(ctx context.Context, p Principal, cursor string, limit int) ([]Conversation, string, error) {
	if err := s.authorize(ctx, p, ActionList, nil); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidArgument)
	}
	return s.cfg.Store.ListConversations(ctx, p, cursor, limit)
}

// Events returns a page of the conversation's history.
func (s *Service) Events(ctx context.Context, p Principal, id string, afterSeq int64, limit int) ([]Event, error) {
	if afterSeq < 0 || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("%w: after must be >= 0 and limit between 1 and 500", ErrInvalidArgument)
	}
	if _, err := s.load(ctx, p, id, ActionRead); err != nil {
		return nil, err
	}
	return s.cfg.Store.ListEvents(ctx, id, afterSeq, limit)
}

// Send starts a turn: message is the user's text and clientMessageID an idempotency key. It
// returns as soon as the turn is recorded and the CLI run is underway. If clientMessageID was
// already used in this conversation it returns that turn with duplicate = true and starts
// nothing.
func (s *Service) Send(ctx context.Context, p Principal, id, message, clientMessageID string) (t Turn, duplicate bool, err error) {
	if !clientMessageIDPattern.MatchString(clientMessageID) {
		return Turn{}, false, fmt.Errorf("%w: clientMessageId must match %s", ErrInvalidArgument, clientMessageIDPattern)
	}
	if message == "" || !utf8.ValidString(message) {
		return Turn{}, false, fmt.Errorf("%w: message must be non-empty UTF-8", ErrInvalidArgument)
	}
	if len(message) > s.cfg.MaxMessageBytes {
		return Turn{}, false, fmt.Errorf("%w: %d bytes, limit %d", ErrMessageTooLarge, len(message), s.cfg.MaxMessageBytes)
	}
	c, err := s.load(ctx, p, id, ActionSend)
	if err != nil {
		return Turn{}, false, err
	}
	if !c.Resumable {
		return Turn{}, false, ErrNotResumable
	}
	if err := s.reserve(p); err != nil {
		s.audit(p, ActionSend, id, "", "too_many_turns")
		return Turn{}, false, err
	}
	released := false
	defer func() {
		if !released {
			s.release(p)
		}
	}()

	turnID, err := s.cfg.NewID()
	if err != nil {
		return Turn{}, false, err
	}
	now := s.cfg.Now()
	t, err = s.cfg.Store.ClaimTurn(ctx, Turn{
		ID: turnID, ConversationID: id, ClientMessageID: clientMessageID,
		Status: TurnRunning, LeaseUntil: now.Add(s.cfg.LeaseDuration), StartedAt: now,
	})
	switch {
	case errors.Is(err, ErrDuplicate):
		return t, true, nil
	case errors.Is(err, ErrTurnInProgress):
		s.audit(p, ActionSend, id, "", "turn_in_progress")
		return Turn{}, false, err
	case err != nil:
		return Turn{}, false, fmt.Errorf("conversation: claim turn: %w", err)
	}

	// The user's message is part of the history from the moment the turn exists.
	w := s.newWriter(id, t.ID)
	w.emit(Event{Kind: stream.KindUserMessage, Text: message})
	w.emit(Event{Kind: stream.KindTurnStarted})

	s.wg.Add(1)
	released = true // runTurn releases the reservation
	go s.runTurn(p, c, t, message, w)
	s.audit(p, ActionSend, id, t.ID, "started")
	return t, false, nil
}

func (s *Service) reserve(p Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perOwner[p] >= s.cfg.MaxActiveTurns {
		return ErrTooManyTurns
	}
	s.perOwner[p]++
	return nil
}

func (s *Service) release(p Principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perOwner[p]--; s.perOwner[p] <= 0 {
		delete(s.perOwner, p)
	}
}

// Cancel asks for a running turn to stop. It works from any instance: the request is recorded
// in the store and the instance running the turn picks it up within CancelPoll. It is
// idempotent. ErrTurnNotRunning if the turn already finished.
func (s *Service) Cancel(ctx context.Context, p Principal, id, turnID string) error {
	if _, err := s.load(ctx, p, id, ActionCancel); err != nil {
		return err
	}
	t, err := s.cfg.Store.GetTurn(ctx, turnID)
	if err != nil || t.ConversationID != id {
		return ErrNotFound
	}
	if err := s.cfg.Store.RequestCancel(ctx, turnID); err != nil {
		return err
	}
	s.mu.Lock()
	cancel := s.running[turnID]
	s.mu.Unlock()
	if cancel != nil {
		cancel(errUserCancel)
	}
	s.audit(p, ActionCancel, id, turnID, "requested")
	return nil
}
