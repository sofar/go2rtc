package viewgroup

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/inference"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGroup(cameras []string, window time.Duration, requireMulti bool) *Group {
	return &Group{
		Name:              "test_group",
		Cameras:           cameras,
		CorrelationWindow: window,
		RequireMultiAngle: requireMulti,
	}
}

func TestCorrelator_CorrelatedDetection(t *testing.T) {
	bus := events.NewBus()
	group := newTestGroup([]string{"cam1", "cam2"}, 3*time.Second, false)
	c := NewCorrelator(group, bus)
	c.Start()
	defer c.Stop()

	// Subscribe to group_detection events
	ch := bus.Subscribe(events.Filter{Type: "group_detection"}, 10)

	// cam1 detects a person
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam1",
		Data: inference.DetectionEvent{
			Camera:     "cam1",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.9}},
			Timestamp:  time.Now(),
		},
	})

	// Small delay, then cam2 detects a person
	time.Sleep(100 * time.Millisecond)
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam2",
		Data: inference.DetectionEvent{
			Camera:     "cam2",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.85}},
			Timestamp:  time.Now(),
		},
	})

	// Should get a correlated group_detection
	select {
	case e := <-ch:
		gde, ok := e.Data.(GroupDetectionEvent)
		require.True(t, ok)
		assert.True(t, gde.Correlated)
		assert.Equal(t, "person", gde.Class)
		assert.Equal(t, "test_group", gde.Group)
		assert.Len(t, gde.Cameras, 2)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for correlated event")
	}
}

func TestCorrelator_DifferentClass_NotCorrelated(t *testing.T) {
	bus := events.NewBus()
	group := newTestGroup([]string{"cam1", "cam2"}, time.Second, false)
	c := NewCorrelator(group, bus)
	c.Start()
	defer c.Stop()

	ch := bus.Subscribe(events.Filter{Type: "group_detection"}, 10)

	// cam1 detects person, cam2 detects car — should not correlate
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam1",
		Data: inference.DetectionEvent{
			Camera:     "cam1",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.9}},
			Timestamp:  time.Now(),
		},
	})
	time.Sleep(50 * time.Millisecond)
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam2",
		Data: inference.DetectionEvent{
			Camera:     "cam2",
			Detections: []inference.Detection{{Class: "car", Confidence: 0.8}},
			Timestamp:  time.Now(),
		},
	})

	// No correlated event expected — only uncorrelated on expiry
	select {
	case e := <-ch:
		gde := e.Data.(GroupDetectionEvent)
		assert.False(t, gde.Correlated, "different classes should not correlate")
	case <-time.After(2 * time.Second):
		// OK — no event is fine too (they'll expire later)
	}
}

func TestCorrelator_IgnoresNonGroupCameras(t *testing.T) {
	bus := events.NewBus()
	group := newTestGroup([]string{"cam1", "cam2"}, time.Second, false)
	c := NewCorrelator(group, bus)
	c.Start()
	defer c.Stop()

	ch := bus.Subscribe(events.Filter{Type: "group_detection"}, 10)

	// cam3 is NOT in the group
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam3",
		Data: inference.DetectionEvent{
			Camera:     "cam3",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.9}},
			Timestamp:  time.Now(),
		},
	})

	time.Sleep(200 * time.Millisecond)

	select {
	case <-ch:
		t.Fatal("should not emit event for non-group camera")
	default:
		// OK
	}
}

func TestCorrelator_RequireMultiAngle_SuppressesSingle(t *testing.T) {
	bus := events.NewBus()
	group := newTestGroup([]string{"cam1", "cam2"}, 500*time.Millisecond, true)
	c := NewCorrelator(group, bus)
	c.Start()
	defer c.Stop()

	ch := bus.Subscribe(events.Filter{Type: "group_detection"}, 10)

	// Only cam1 detects — should be suppressed with require_multi_angle
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam1",
		Data: inference.DetectionEvent{
			Camera:     "cam1",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.9}},
			Timestamp:  time.Now(),
		},
	})

	// Wait for expiry + flush
	time.Sleep(time.Second)

	select {
	case <-ch:
		t.Fatal("should suppress single-camera detection when require_multi_angle is true")
	default:
		// OK — suppressed
	}
}

func TestCorrelator_SameCamera_NotCorrelated(t *testing.T) {
	bus := events.NewBus()
	group := newTestGroup([]string{"cam1", "cam2"}, time.Second, false)
	c := NewCorrelator(group, bus)
	c.Start()
	defer c.Stop()

	ch := bus.Subscribe(events.Filter{Type: "group_detection"}, 10)

	// cam1 detects person twice — same camera should not self-correlate
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam1",
		Data: inference.DetectionEvent{
			Camera:     "cam1",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.9}},
			Timestamp:  time.Now(),
		},
	})
	time.Sleep(50 * time.Millisecond)
	bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: "cam1",
		Data: inference.DetectionEvent{
			Camera:     "cam1",
			Detections: []inference.Detection{{Class: "person", Confidence: 0.85}},
			Timestamp:  time.Now(),
		},
	})

	time.Sleep(200 * time.Millisecond)

	// Should not get a correlated event
	select {
	case e := <-ch:
		gde := e.Data.(GroupDetectionEvent)
		if gde.Correlated {
			t.Fatal("same camera should not self-correlate")
		}
	default:
		// OK
	}
}

func TestInGroup(t *testing.T) {
	c := &Correlator{
		group: &Group{Cameras: []string{"cam1", "cam2", "cam3"}},
	}
	assert.True(t, c.inGroup("cam1"))
	assert.True(t, c.inGroup("cam3"))
	assert.False(t, c.inGroup("cam4"))
}
