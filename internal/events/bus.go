// Package events provides an in-process publish/subscribe event bus for
// camera state changes, detection events, and recording triggers.
//
// The bus supports typed filtering so subscribers only receive events
// they care about. External export (MQTT, webhooks) is handled by
// subscribers registered during Init().
package events

import (
	"sync"
	"time"
)

// Event is the common envelope for all events on the bus.
type Event struct {
	Type      string    `json:"type"`
	Camera    string    `json:"camera,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Well-known event types.
const (
	TypeDetection  = "detection"
	TypeStateChange = "state_change"
	TypeRecording  = "recording"
)

// Filter controls which events a subscriber receives. Zero-value
// fields match everything.
type Filter struct {
	Type   string // match event type, empty = all
	Camera string // match camera name, empty = all
}

func (f Filter) matches(e *Event) bool {
	if f.Type != "" && f.Type != e.Type {
		return false
	}
	if f.Camera != "" && f.Camera != e.Camera {
		return false
	}
	return true
}

// subscriber is an internal registration.
type subscriber struct {
	filter Filter
	ch     chan *Event
}

// Bus is the central event dispatcher.
type Bus struct {
	mu          sync.RWMutex
	subscribers []*subscriber
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{}
}

// Publish sends an event to all matching subscribers. Non-blocking: if
// a subscriber's channel is full, the event is dropped for that subscriber.
func (b *Bus) Publish(e *Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, s := range b.subscribers {
		if s.filter.matches(e) {
			select {
			case s.ch <- e:
			default:
				// drop — subscriber is slow
			}
		}
	}
}

// Subscribe returns a channel that receives events matching the filter.
// bufSize controls the channel buffer (default 64 if <= 0).
func (b *Bus) Subscribe(filter Filter, bufSize int) <-chan *Event {
	if bufSize <= 0 {
		bufSize = 64
	}

	ch := make(chan *Event, bufSize)
	s := &subscriber{filter: filter, ch: ch}

	b.mu.Lock()
	b.subscribers = append(b.subscribers, s)
	b.mu.Unlock()

	return ch
}

// Unsubscribe removes a subscription by its channel. The channel is
// closed after removal.
func (b *Bus) Unsubscribe(ch <-chan *Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, s := range b.subscribers {
		if s.ch == ch {
			close(s.ch)
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}
