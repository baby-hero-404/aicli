package conversation

import (
	"context"
	"time"
)

const catchUpPage = 500

// Subscribe streams the conversation's events with Seq > afterSeq: first the stored history,
// then live events, each exactly once and in order. The channel is closed when ctx ends, when
// the principal loses read access, or when the service closes.
//
// Replay and live delivery are joined without a gap or a duplicate: the live subscription is
// registered before the store is read, so nothing published meanwhile is missed, and every
// event is checked against the highest Seq already delivered, so nothing is repeated. A
// subscriber that falls behind, or that follows a turn running on another instance, catches up
// from the store: when the broker reports it dropped events, on a gap in Seq, and every EventPoll.
func (s *Service) Subscribe(ctx context.Context, p Principal, id string, afterSeq int64) (<-chan Event, error) {
	if afterSeq < 0 {
		return nil, ErrInvalidArgument
	}
	if _, err := s.load(ctx, p, id, ActionRead); err != nil {
		return nil, err
	}
	sub := s.broker.subscribe(id) // before reading the store
	out := make(chan Event, 64)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(out)
		defer s.broker.unsubscribe(id, sub)
		s.follow(ctx, p, id, afterSeq, sub, out)
	}()
	return out, nil
}

func (s *Service) follow(ctx context.Context, p Principal, id string, last int64, sub *subscriber, out chan<- Event) {
	send := func(e Event) bool {
		select {
		case out <- e:
			last = e.Seq
			return true
		case <-ctx.Done():
			return false
		case <-s.baseCtx.Done():
			return false
		}
	}
	// catchUp delivers everything stored after last. It returns false when the subscription
	// must end.
	catchUp := func() bool {
		for {
			evs, err := s.cfg.Store.ListEvents(ctx, id, last, catchUpPage)
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				s.log.Warn("conversation: subscribe: read events", "conversation_id", id, "err", err)
				return true // try again on the next tick
			}
			for _, e := range evs {
				if !send(e) {
					return false
				}
			}
			if len(evs) < catchUpPage {
				return true
			}
		}
	}

	if !catchUp() {
		return
	}
	poll := time.NewTicker(s.cfg.EventPoll)
	defer poll.Stop()
	reauth := time.NewTicker(s.cfg.ReauthInterval)
	defer reauth.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.baseCtx.Done():
			return
		case e := <-sub.ch:
			switch {
			case e.Seq <= last: // already delivered by the replay
			case e.Seq == last+1:
				if !send(e) {
					return
				}
			default: // a gap: events were dropped for us or come from another instance
				if !catchUp() {
					return
				}
			}
		case <-sub.lag:
			if !catchUp() {
				return
			}
		case <-poll.C:
			if !catchUp() {
				return
			}
		case <-reauth.C:
			if _, err := s.load(ctx, p, id, ActionRead); err != nil {
				return
			}
		}
	}
}
