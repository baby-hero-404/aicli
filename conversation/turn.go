package conversation

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/run/stream"
)

const (
	writerBatch    = 32
	writerInterval = 100 * time.Millisecond
	storeTimeout   = 30 * time.Second
)

// writer persists a turn's events in batches and then hands them to live subscribers. Events
// are stored first so that anything a subscriber sees can also be replayed.
type writer struct {
	s              *Service
	conversationID string
	turnID         string
	ch             chan Event
	done           chan struct{}

	mu     sync.Mutex // guards closed and the send on ch against close
	closed bool
}

func (s *Service) newWriter(conversationID, turnID string) *writer {
	w := &writer{s: s, conversationID: conversationID, turnID: turnID, ch: make(chan Event, 256), done: make(chan struct{})}
	go w.loop()
	return w
}

// emit queues an event. It blocks when the queue is full, which slows the CLI's output
// reader rather than losing events. An event emitted after close is dropped: a runtime that
// misbehaves and delivers output late must not be able to crash the host.
func (w *writer) emit(e Event) {
	e.TurnID = w.turnID
	e.Time = w.s.cfg.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.ch <- e
}

// close waits until everything queued has been stored. It is safe to call more than once.
func (w *writer) close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *writer) loop() {
	defer close(w.done)
	tick := time.NewTicker(writerInterval)
	defer tick.Stop()
	var batch []Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()
		stored, err := w.s.cfg.Store.AppendEvents(ctx, w.conversationID, batch)
		if err != nil {
			w.s.log.Error("conversation: store events", "conversation_id", w.conversationID, "turn_id", w.turnID, "err", err)
		} else {
			w.s.broker.publish(w.conversationID, stored)
		}
		batch = batch[:0]
	}
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= writerBatch {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

// outcome is how a turn ended.
type outcome struct {
	status     TurnStatus
	failure    FailureCode
	retryAfter time.Duration
	sessionID  string // CLI session to remember ("" keeps the stored one)
	resumable  bool
}

func (s *Service) runTurn(p Principal, c Conversation, t Turn, message string, w *writer) {
	defer s.wg.Done()
	defer s.release(p)

	ctx, cancel := context.WithCancelCause(s.baseCtx)
	defer cancel(nil)
	s.mu.Lock()
	s.running[t.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, t.ID)
		s.mu.Unlock()
	}()

	watchCtx, stopWatch := context.WithCancel(context.Background())
	var watchDone sync.WaitGroup
	watchDone.Add(1)
	go func() { defer watchDone.Done(); s.watch(watchCtx, t, cancel) }()

	var usage map[string]string
	out, credsNotSaved := s.execute(ctx, p, c, t, message, w, &usage)

	stopWatch()
	watchDone.Wait()

	t.Status, t.Failure, t.RetryAfter, t.CredentialsNotSaved = out.status, out.failure, out.retryAfter, credsNotSaved
	t.FinishedAt = s.cfg.Now()
	final := Event{Kind: kindOf(out.status), Meta: map[string]string{}}
	for k, v := range usage {
		final.Meta[k] = v
	}
	if out.failure != "" {
		final.Meta["failure"] = string(out.failure)
	}
	if out.retryAfter > 0 {
		final.Meta["retry_after_seconds"] = strconv.Itoa(int(out.retryAfter.Seconds()))
	}
	if credsNotSaved {
		final.Meta["credentials_not_saved"] = "true"
	}
	w.emit(final)
	w.close() // the terminal event is stored before the conversation becomes idle

	fctx, fcancel := context.WithTimeout(context.Background(), storeTimeout)
	defer fcancel()
	if err := s.cfg.Store.FinishTurn(fctx, t, out.sessionID, out.resumable); err != nil {
		s.log.Error("conversation: finish turn", "conversation_id", c.ID, "turn_id", t.ID, "err", err)
	}
	s.audit(p, ActionSend, c.ID, t.ID, string(out.status))
}

func kindOf(s TurnStatus) stream.Kind {
	switch s {
	case TurnCompleted:
		return stream.KindTurnCompleted
	case TurnCancelled:
		return stream.KindTurnCancelled
	}
	return stream.KindTurnFailed
}

// watch renews the turn's lease and applies cancel requests recorded by any instance.
func (s *Service) watch(ctx context.Context, t Turn, cancel context.CancelCauseFunc) {
	renew := time.NewTicker(s.cfg.LeaseDuration / 4)
	defer renew.Stop()
	poll := time.NewTicker(s.cfg.CancelPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			if err := s.cfg.Store.RenewLease(ctx, t.ID, s.cfg.Now().Add(s.cfg.LeaseDuration)); err != nil && ctx.Err() == nil {
				s.log.Warn("conversation: renew lease", "turn_id", t.ID, "err", err)
			}
		case <-poll.C:
			ids, err := s.cfg.Store.CancelRequested(ctx, []string{t.ID})
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("conversation: poll cancel", "turn_id", t.ID, "err", err)
				}
				continue
			}
			if len(ids) > 0 {
				cancel(errUserCancel)
				return
			}
		}
	}
}

// execute resolves and runs the turn and works out how it ended. The second result is true
// when refreshed credentials could not be saved.
func (s *Service) execute(ctx context.Context, p Principal, c Conversation, t Turn, message string, w *writer, usage *map[string]string) (outcome, bool) {
	prof, ok := profile.Lookup(c.Profile)
	if !ok {
		s.log.Error("conversation: unknown profile", "conversation_id", c.ID, "profile", c.Profile)
		return failed(FailRunFailed, c), false
	}
	base, err := s.cfg.Resolver.Resolve(ctx, p, c)
	if err != nil {
		s.log.Error("conversation: resolve", "conversation_id", c.ID, "turn_id", t.ID, "err", err)
		return failed(FailRunFailed, c), false
	}
	// Everything the client could influence is set here, over whatever the resolver returned.
	base.Profile, base.Command, base.Args, base.ExtraArgs = c.Profile, prof.Command, prof.Args, nil
	base.Prompt, base.ResumeSessionID = message, c.SessionID
	base.OnEvent = func(e stream.Event) { forward(w, e, usage) }

	res, runErr := s.cfg.Runner.Run(ctx, base)

	credsNotSaved := false
	if res != nil && len(res.UpdatedCredentialFiles) > 0 {
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		if err := s.cfg.Resolver.SaveCredentials(sctx, p, c, res.UpdatedCredentialFiles); err != nil {
			credsNotSaved = true
			s.log.Error("conversation: save refreshed credentials", "conversation_id", c.ID, "turn_id", t.ID, "err", err)
		}
		scancel()
	}
	return s.classify(ctx, c, res, runErr), credsNotSaved
}

// forward passes a decoded CLI event to the writer. The turn's own start and end events are
// produced by the service from run.Result, which is authoritative; the CLI's are only mined
// for usage numbers.
func forward(w *writer, e stream.Event, usage *map[string]string) {
	switch e.Kind {
	case stream.KindTurnStarted:
	case stream.KindTurnCompleted, stream.KindTurnFailed:
		if len(e.Meta) > 0 {
			m := map[string]string{}
			for _, k := range []string{"input_tokens", "output_tokens", "total_tokens"} {
				if v, ok := e.Meta[k]; ok {
					m[k] = v
				}
			}
			*usage = m
		}
	default:
		w.emit(Event{Kind: e.Kind, Text: e.Text, Tool: e.Tool, Meta: e.Meta})
	}
}

func failed(code FailureCode, c Conversation) outcome {
	return outcome{status: TurnFailed, failure: code, resumable: c.Resumable}
}

func (s *Service) classify(ctx context.Context, c Conversation, res *run.Result, runErr error) outcome {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errUserCancel):
		return outcome{status: TurnCancelled, resumable: c.Resumable, sessionID: sessionOf(res)}
	case errors.Is(cause, errShutdown):
		return failed(FailInterrupted, c)
	}
	if runErr != nil {
		s.log.Error("conversation: run failed", "conversation_id", c.ID, "err", runErr)
		return failed(FailRunFailed, c)
	}
	switch {
	case res.SessionMismatch:
		// The CLI started a new session for an ID it no longer knows. Its ID is discarded.
		return outcome{status: TurnFailed, failure: FailSessionExpired, resumable: false}
	case res.QuotaExceeded:
		return outcome{status: TurnFailed, failure: FailQuotaExceeded, retryAfter: res.QuotaCooldown, resumable: c.Resumable, sessionID: res.SessionID}
	case res.AuthInvalid:
		return outcome{status: TurnFailed, failure: FailAuthInvalid, resumable: c.Resumable, sessionID: res.SessionID}
	case res.AwaitingInput:
		return outcome{status: TurnFailed, failure: FailAwaitingInput, resumable: c.Resumable, sessionID: res.SessionID}
	case !res.Success:
		return outcome{status: TurnFailed, failure: FailRunFailed, resumable: c.Resumable, sessionID: res.SessionID}
	}
	return outcome{status: TurnCompleted, resumable: true, sessionID: res.SessionID}
}

func sessionOf(res *run.Result) string {
	if res == nil || res.SessionMismatch {
		return ""
	}
	return res.SessionID
}

// RecoverStale marks turns whose owner went away (lease expired) as interrupted, so the
// conversation can be used again. Call it at startup and periodically. It returns how many
// turns it recovered.
func (s *Service) RecoverStale(ctx context.Context) (int, error) {
	stale, err := s.cfg.Store.StaleTurns(ctx, s.cfg.Now())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range stale {
		s.mu.Lock()
		_, local := s.running[t.ID]
		s.mu.Unlock()
		if local {
			continue
		}
		c, err := s.cfg.Store.GetConversation(ctx, t.ConversationID)
		if err != nil {
			s.log.Error("conversation: recover: load conversation", "turn_id", t.ID, "err", err)
			continue
		}
		ev := Event{TurnID: t.ID, Time: s.cfg.Now(), Kind: stream.KindTurnFailed, Meta: map[string]string{"failure": string(FailInterrupted)}}
		stored, err := s.cfg.Store.AppendEvents(ctx, t.ConversationID, []Event{ev})
		if err != nil {
			s.log.Error("conversation: recover: store event", "turn_id", t.ID, "err", err)
			continue
		}
		s.broker.publish(t.ConversationID, stored)
		t.Status, t.Failure, t.FinishedAt = TurnFailed, FailInterrupted, s.cfg.Now()
		if err := s.cfg.Store.FinishTurn(ctx, t, "", c.Resumable); err != nil {
			s.log.Error("conversation: recover: finish turn", "turn_id", t.ID, "err", err)
			continue
		}
		s.audit(Principal(""), ActionSend, t.ConversationID, t.ID, "recovered")
		n++
	}
	return n, nil
}
