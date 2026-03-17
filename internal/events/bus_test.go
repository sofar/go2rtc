package events

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPublish_DeliveredToSubscriber(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: "hello"})

	select {
	case e := <-ch:
		assert.Equal(t, TypeDetection, e.Type)
		assert.Equal(t, "cam1", e.Camera)
		assert.Equal(t, "hello", e.Data)
		assert.False(t, e.Timestamp.IsZero())
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestPublish_FilterByType(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{Type: TypeDetection}, 10)

	bus.Publish(&Event{Type: TypeStateChange, Camera: "cam1"})
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})

	select {
	case e := <-ch:
		assert.Equal(t, TypeDetection, e.Type)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Should not have the state_change event
	select {
	case <-ch:
		t.Fatal("should not receive state_change event")
	default:
	}
}

func TestPublish_FilterByCamera(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{Camera: "cam2"}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam2"})

	select {
	case e := <-ch:
		assert.Equal(t, "cam2", e.Camera)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	select {
	case <-ch:
		t.Fatal("should not receive cam1 event")
	default:
	}
}

func TestPublish_FilterByCameraAndType(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{Type: TypeDetection, Camera: "cam1"}, 10)

	bus.Publish(&Event{Type: TypeStateChange, Camera: "cam1"})
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam2"})
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})

	select {
	case e := <-ch:
		assert.Equal(t, TypeDetection, e.Type)
		assert.Equal(t, "cam1", e.Camera)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	select {
	case <-ch:
		t.Fatal("should not receive non-matching events")
	default:
	}
}

func TestPublish_EmptyFilterMatchesAll(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})
	bus.Publish(&Event{Type: TypeStateChange, Camera: "cam2"})

	e1 := <-ch
	e2 := <-ch
	assert.Equal(t, TypeDetection, e1.Type)
	assert.Equal(t, TypeStateChange, e2.Type)
}

func TestPublish_MultipleSubscribers(t *testing.T) {
	bus := NewBus()
	ch1 := bus.Subscribe(Filter{}, 10)
	ch2 := bus.Subscribe(Filter{}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})

	e1 := <-ch1
	e2 := <-ch2
	assert.Equal(t, e1.Type, e2.Type)
	assert.Equal(t, e1.Camera, e2.Camera)
}

func TestUnsubscribe(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	bus.Unsubscribe(ch)

	// Channel should be closed
	_, ok := <-ch
	assert.False(t, ok)
}

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	bus.Unsubscribe(ch)

	// Publishing after unsubscribe should not panic
	bus.Publish(&Event{Type: TypeDetection})
}

func TestPublish_DropsOnFullBuffer(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 2) // tiny buffer

	// Fill the buffer
	bus.Publish(&Event{Type: "1"})
	bus.Publish(&Event{Type: "2"})

	// This should be dropped, not block
	bus.Publish(&Event{Type: "3"})

	e1 := <-ch
	e2 := <-ch
	assert.Equal(t, "1", e1.Type)
	assert.Equal(t, "2", e2.Type)

	// Buffer should be empty now
	select {
	case <-ch:
		t.Fatal("should not have a third event")
	default:
	}
}

func TestPublish_SetsTimestamp(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	before := time.Now()
	bus.Publish(&Event{Type: TypeDetection})
	after := time.Now()

	e := <-ch
	assert.True(t, !e.Timestamp.Before(before))
	assert.True(t, !e.Timestamp.After(after))
}

func TestPublish_PreservesExistingTimestamp(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 10)

	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bus.Publish(&Event{Type: TypeDetection, Timestamp: ts})

	e := <-ch
	assert.Equal(t, ts, e.Timestamp)
}

func TestPublish_ConcurrentSafe(t *testing.T) {
	bus := NewBus()
	ch := bus.Subscribe(Filter{}, 1000)

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			bus.Publish(&Event{Type: TypeDetection, Camera: "cam1"})
		}(i)
	}
	wg.Wait()

	count := len(ch)
	assert.Equal(t, 100, count)
}

func TestFilter_Matches(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		event  Event
		match  bool
	}{
		{"empty matches all", Filter{}, Event{Type: "x", Camera: "y"}, true},
		{"type match", Filter{Type: "x"}, Event{Type: "x"}, true},
		{"type mismatch", Filter{Type: "x"}, Event{Type: "y"}, false},
		{"camera match", Filter{Camera: "c"}, Event{Camera: "c"}, true},
		{"camera mismatch", Filter{Camera: "c"}, Event{Camera: "d"}, false},
		{"both match", Filter{Type: "x", Camera: "c"}, Event{Type: "x", Camera: "c"}, true},
		{"type match camera miss", Filter{Type: "x", Camera: "c"}, Event{Type: "x", Camera: "d"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.match, tt.filter.matches(&tt.event))
		})
	}
}
