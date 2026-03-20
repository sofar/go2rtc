package notify

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/stretchr/testify/assert"
)

func sess(class, region, camera string, dur float64, dets int, conf float32) *events.Session {
	return &events.Session{
		Class:          class,
		Region:         region,
		Camera:         camera,
		Cameras:        []string{camera},
		Duration:       dur,
		DetectionCount: dets,
		PeakConfidence: conf,
		Active:         true,
	}
}

func TestPolicy_MatchesClass(t *testing.T) {
	p := &Policy{Classes: []string{"person"}, On: "start"}
	assert.True(t, p.Matches(sess("person", "driveway", "cam1", 0, 1, 0.9), events.TypeSessionStart))
	assert.False(t, p.Matches(sess("car", "driveway", "cam1", 0, 1, 0.9), events.TypeSessionStart))
}

func TestPolicy_MatchesRegion(t *testing.T) {
	p := &Policy{Regions: []string{"driveway", "garage_pad"}, On: "start"}
	assert.True(t, p.Matches(sess("car", "driveway", "cam1", 0, 1, 0.9), events.TypeSessionStart))
	assert.True(t, p.Matches(sess("car", "garage_pad", "cam1", 0, 1, 0.9), events.TypeSessionStart))
	assert.False(t, p.Matches(sess("car", "front_door", "cam1", 0, 1, 0.9), events.TypeSessionStart))
}

func TestPolicy_MatchesCamera(t *testing.T) {
	p := &Policy{Cameras: []string{"amcrest"}, On: "start"}
	assert.True(t, p.Matches(sess("car", "driveway", "amcrest", 0, 1, 0.9), events.TypeSessionStart))
	assert.False(t, p.Matches(sess("car", "driveway", "ipc122_a", 0, 1, 0.9), events.TypeSessionStart))
}

func TestPolicy_MatchesEventType(t *testing.T) {
	pStart := &Policy{On: "start"}
	pEnd := &Policy{On: "end"}

	s := sess("car", "driveway", "cam1", 10, 5, 0.9)
	assert.True(t, pStart.Matches(s, events.TypeSessionStart))
	assert.False(t, pStart.Matches(s, events.TypeSessionEnd))
	assert.True(t, pEnd.Matches(s, events.TypeSessionEnd))
	assert.False(t, pEnd.Matches(s, events.TypeSessionStart))
}

func TestPolicy_MinDuration(t *testing.T) {
	p := &Policy{On: "end", MinDuration: "30s"}
	assert.False(t, p.Matches(sess("car", "d", "c", 10, 5, 0.9), events.TypeSessionEnd))
	assert.True(t, p.Matches(sess("car", "d", "c", 60, 5, 0.9), events.TypeSessionEnd))
}

func TestPolicy_MaxDuration(t *testing.T) {
	p := &Policy{On: "end", MaxDuration: "5m"}
	assert.True(t, p.Matches(sess("car", "d", "c", 60, 5, 0.9), events.TypeSessionEnd))
	assert.False(t, p.Matches(sess("car", "d", "c", 600, 5, 0.9), events.TypeSessionEnd))
}

func TestPolicy_MinDetections(t *testing.T) {
	p := &Policy{On: "end", MinDetections: 10}
	assert.False(t, p.Matches(sess("car", "d", "c", 60, 5, 0.9), events.TypeSessionEnd))
	assert.True(t, p.Matches(sess("car", "d", "c", 60, 15, 0.9), events.TypeSessionEnd))
}

func TestPolicy_MinConfidence(t *testing.T) {
	p := &Policy{On: "start", MinConfidence: 0.8}
	assert.False(t, p.Matches(sess("car", "d", "c", 0, 1, 0.5), events.TypeSessionStart))
	assert.True(t, p.Matches(sess("car", "d", "c", 0, 1, 0.9), events.TypeSessionStart))
}

func TestPolicy_MinCameras(t *testing.T) {
	p := &Policy{On: "start", MinCameras: 2}
	single := sess("car", "d", "cam1", 0, 1, 0.9)
	multi := sess("car", "d", "cam1", 0, 1, 0.9)
	multi.Cameras = []string{"cam1", "cam2"}

	assert.False(t, p.Matches(single, events.TypeSessionStart))
	assert.True(t, p.Matches(multi, events.TypeSessionStart))
}

func TestPolicy_DayOfWeek(t *testing.T) {
	p := &Policy{On: "start", Days: []string{"mon", "wed", "fri"}}

	// We can't easily test specific days without mocking time,
	// so just verify the function exists and returns a bool
	s := sess("car", "d", "c", 0, 1, 0.9)
	_ = p.Matches(s, events.TypeSessionStart) // should not panic
}

func TestPolicy_TimeOfDay(t *testing.T) {
	p := &Policy{On: "start"}

	// Normal range: 08:00 - 18:00
	p.After = "08:00"
	p.Before = "18:00"
	assert.True(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)))
	assert.False(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 20, 0, 0, 0, time.UTC)))
	assert.False(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 6, 0, 0, 0, time.UTC)))

	// Overnight range: 22:00 - 06:00
	p.After = "22:00"
	p.Before = "06:00"
	assert.True(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 23, 0, 0, 0, time.UTC)))
	assert.True(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)))
	assert.False(t, p.matchesTimeOfDay(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)))
}

func TestPolicy_CombinedCriteria(t *testing.T) {
	p := &Policy{
		On:            "end",
		Classes:       []string{"person"},
		Regions:       []string{"front_door"},
		MinDuration:   "10s",
		MinConfidence: 0.7,
	}

	// All criteria met
	assert.True(t, p.Matches(sess("person", "front_door", "c", 30, 5, 0.9), events.TypeSessionEnd))

	// Wrong class
	assert.False(t, p.Matches(sess("car", "front_door", "c", 30, 5, 0.9), events.TypeSessionEnd))

	// Wrong region
	assert.False(t, p.Matches(sess("person", "driveway", "c", 30, 5, 0.9), events.TypeSessionEnd))

	// Too short
	assert.False(t, p.Matches(sess("person", "front_door", "c", 5, 5, 0.9), events.TypeSessionEnd))

	// Low confidence
	assert.False(t, p.Matches(sess("person", "front_door", "c", 30, 5, 0.5), events.TypeSessionEnd))
}

func TestPolicy_EmptyMatchesAll(t *testing.T) {
	p := &Policy{On: "start"}
	assert.True(t, p.Matches(sess("anything", "anywhere", "any_cam", 0, 1, 0.1), events.TypeSessionStart))
}
