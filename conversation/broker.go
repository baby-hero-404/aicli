package conversation

import "sync"

// subscriberBuffer is how many live events a subscriber may fall behind before the broker
// starts dropping for it. A dropped event is not lost: the subscriber sees a gap in Seq and
// reads the missing events from the store.
const subscriberBuffer = 256

// broker fans events out to in-process subscribers. It is only a latency optimization over
// reading the store: it never blocks a publisher and may drop events for a slow subscriber.
type broker struct {
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	ch chan Event
	// lag is signalled (without blocking) whenever an event was dropped for this subscriber,
	// so it catches up from the store at once instead of waiting for its next poll or event.
	lag chan struct{}
}

func newBroker() *broker {
	return &broker{subs: map[string]map[*subscriber]struct{}{}}
}

func (b *broker) subscribe(conversationID string) *subscriber {
	s := &subscriber{ch: make(chan Event, subscriberBuffer), lag: make(chan struct{}, 1)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[conversationID] == nil {
		b.subs[conversationID] = map[*subscriber]struct{}{}
	}
	b.subs[conversationID][s] = struct{}{}
	return s
}

func (b *broker) unsubscribe(conversationID string, s *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs[conversationID], s)
	if len(b.subs[conversationID]) == 0 {
		delete(b.subs, conversationID)
	}
}

// publish hands events to every subscriber of the conversation without blocking.
func (b *broker) publish(conversationID string, events []Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs[conversationID] {
		for _, e := range events {
			select {
			case s.ch <- e:
			default: // slow subscriber: tell it to catch up from the store
				select {
				case s.lag <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (b *broker) count(conversationID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[conversationID])
}
