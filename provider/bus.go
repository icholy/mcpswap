package provider

import (
	"context"
	"sync"
)

// Key identifies a value in the global namespace formed by every
// provider's type prefix and its declared names.
type Key struct {
	Type string // matches Provider.Type()
	Name string // a name claimed by that provider
}

// Event is published on the Bus when a value rotates.
type Event struct {
	Key   Key
	Value string
}

// Bus carries rotation events. Providers Publish to it; subscribers
// receive Events for keys they registered.
type Bus struct {
	mu   sync.Mutex
	subs map[*subscription]struct{}
}

type subscription struct {
	keys map[Key]struct{}
	// pending holds the latest Event per key not yet delivered. Coalesces
	// bursts: only the most recent value for each key is kept.
	mu      sync.Mutex
	pending map[Key]string
	wake    chan struct{}
	out     chan Event
}

// NewBus creates an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: map[*subscription]struct{}{}}
}

// Publish announces that key rotated to value. Non-blocking with
// respect to slow subscribers; if a subscriber's pending slot for
// this key already holds a value, it's overwritten with the latest.
func (b *Bus) Publish(k Key, value string) {
	b.mu.Lock()
	subs := make([]*subscription, 0, len(b.subs))
	for s := range b.subs {
		if _, ok := s.keys[k]; ok {
			subs = append(subs, s)
		}
	}
	b.mu.Unlock()
	for _, s := range subs {
		s.publish(k, value)
	}
}

// Subscribe returns a channel that emits Events for any of keys.
// Channel closes when ctx is canceled. Late subscribers do not see
// prior publishes.
func (b *Bus) Subscribe(ctx context.Context, keys []Key) <-chan Event {
	s := &subscription{
		keys:    make(map[Key]struct{}, len(keys)),
		pending: map[Key]string{},
		wake:    make(chan struct{}, 1),
		out:     make(chan Event, len(keys)+1),
	}
	for _, k := range keys {
		s.keys[k] = struct{}{}
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go s.run(ctx, b)
	return s.out
}

func (s *subscription) publish(k Key, value string) {
	s.mu.Lock()
	s.pending[k] = value
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscription) drain() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := make([]Event, 0, len(s.pending))
	for k, v := range s.pending {
		out = append(out, Event{Key: k, Value: v})
	}
	s.pending = map[Key]string{}
	return out
}

func (s *subscription) run(ctx context.Context, b *Bus) {
	defer func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
		close(s.out)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		for _, ev := range s.drain() {
			select {
			case <-ctx.Done():
				return
			case s.out <- ev:
			}
		}
	}
}
