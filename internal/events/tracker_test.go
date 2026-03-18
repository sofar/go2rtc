package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDetectionData is a test payload that the parser can handle.
type mockDetectionData struct {
	Region     string
	Detections []struct {
		Class      string
		Confidence float32
	}
}

func setupTrackerTest(quietDur time.Duration) (*Bus, *Tracker) {
	bus := NewBus()

	// Register a parser for our mock data
	RegisterDetectionParser(func(data any) []DetectionDetail {
		d, ok := data.(mockDetectionData)
		if !ok {
			return nil
		}
		details := make([]DetectionDetail, len(d.Detections))
		for i, det := range d.Detections {
			details[i] = DetectionDetail{
				Region:     d.Region,
				Class:      det.Class,
				Confidence: det.Confidence,
			}
		}
		return details
	})

	tracker := NewTracker(bus, quietDur, 1)
	tracker.Start()
	return bus, tracker
}

func det(class string, conf float32) struct {
	Class      string
	Confidence float32
} {
	return struct {
		Class      string
		Confidence float32
	}{class, conf}
}

func TestTracker_SessionStartsOnFirstDetection(t *testing.T) {
	bus, tracker := setupTrackerTest(time.Second)
	defer tracker.Stop()

	ch := bus.Subscribe(Filter{Type: TypeSessionStart}, 10)

	bus.Publish(&Event{
		Type:   TypeDetection,
		Camera: "cam1",
		Data: mockDetectionData{
			Region:     "driveway",
			Detections: []struct{ Class string; Confidence float32 }{det("person", 0.9)},
		},
	})

	select {
	case e := <-ch:
		s := e.Data.(*Session)
		assert.Equal(t, "driveway", s.Region)
		assert.Equal(t, "person", s.Class)
		assert.Equal(t, "cam1", s.Camera)
		assert.True(t, s.Active)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for session_start")
	}

	active := tracker.ActiveSessions()
	require.Len(t, active, 1)
	assert.Equal(t, "person", active[0].Class)
}

func TestTracker_SessionExtendsOnSubsequentDetections(t *testing.T) {
	bus, tracker := setupTrackerTest(time.Second)
	defer tracker.Stop()

	// Drain session_start
	ch := bus.Subscribe(Filter{Type: TypeSessionStart}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("car", 0.7)},
	}})
	<-ch // wait for start

	// Second detection with higher confidence
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("car", 0.95)},
	}})

	time.Sleep(100 * time.Millisecond)

	active := tracker.ActiveSessions()
	require.Len(t, active, 1)
	assert.Equal(t, 2, active[0].DetectionCount)
	assert.InDelta(t, 0.95, float64(active[0].PeakConfidence), 0.01)
}

func TestTracker_SessionEndsAfterQuietPeriod(t *testing.T) {
	bus, tracker := setupTrackerTest(500 * time.Millisecond)
	defer tracker.Stop()

	endCh := bus.Subscribe(Filter{Type: TypeSessionEnd}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("person", 0.8)},
	}})

	// Wait for quiet period + expiry tick
	select {
	case e := <-endCh:
		s := e.Data.(*Session)
		assert.Equal(t, "person", s.Class)
		assert.Equal(t, "driveway", s.Region)
		assert.False(t, s.Active)
		assert.Greater(t, s.Duration, 0.0)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for session_end")
	}

	assert.Empty(t, tracker.ActiveSessions())
	assert.Len(t, tracker.RecentSessions(), 1)
}

func TestTracker_SameRegionDifferentCamerasMerge(t *testing.T) {
	bus, tracker := setupTrackerTest(time.Second)
	defer tracker.Stop()

	startCh := bus.Subscribe(Filter{Type: TypeSessionStart}, 10)

	// cam1 detects car in driveway
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("car", 0.8)},
	}})
	<-startCh

	// cam2 also detects car in driveway — should extend, not create new
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam2", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("car", 0.9)},
	}})

	time.Sleep(100 * time.Millisecond)

	active := tracker.ActiveSessions()
	require.Len(t, active, 1, "same region+class from different cameras should merge")
	assert.Equal(t, 2, active[0].DetectionCount)
}

func TestTracker_DifferentClassesSeparateSessions(t *testing.T) {
	bus, tracker := setupTrackerTest(time.Second)
	defer tracker.Stop()

	startCh := bus.Subscribe(Filter{Type: TypeSessionStart}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{
			det("person", 0.9), det("car", 0.8),
		},
	}})

	<-startCh
	<-startCh // two starts

	active := tracker.ActiveSessions()
	assert.Len(t, active, 2)
}

func TestTracker_DifferentRegionsSeparateSessions(t *testing.T) {
	bus, tracker := setupTrackerTest(time.Second)
	defer tracker.Stop()

	startCh := bus.Subscribe(Filter{Type: TypeSessionStart}, 10)

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "driveway", Detections: []struct{ Class string; Confidence float32 }{det("person", 0.9)},
	}})
	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: mockDetectionData{
		Region: "garage", Detections: []struct{ Class string; Confidence float32 }{det("person", 0.8)},
	}})

	<-startCh
	<-startCh

	active := tracker.ActiveSessions()
	assert.Len(t, active, 2)
}

func TestTracker_NoParserNoSessions(t *testing.T) {
	bus := NewBus()
	old := detectionParser
	detectionParser = nil
	defer func() { detectionParser = old }()

	tracker := NewTracker(bus, time.Second, 1)
	tracker.Start()
	defer tracker.Stop()

	bus.Publish(&Event{Type: TypeDetection, Camera: "cam1", Data: "not a detection"})
	time.Sleep(100 * time.Millisecond)

	assert.Empty(t, tracker.ActiveSessions())
}
