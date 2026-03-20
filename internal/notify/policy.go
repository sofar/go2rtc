package notify

import (
	"fmt"
	"time"

	"github.com/AlexxIT/go2rtc/internal/events"
)

// Policy defines when a notification should be sent.
// All non-zero fields must match (AND logic). Zero/empty fields
// are ignored (match everything).
type Policy struct {
	// What to match
	Cameras  []string `yaml:"cameras"`  // camera names, empty = all
	Regions  []string `yaml:"regions"`  // region names, empty = all
	Classes  []string `yaml:"classes"`  // object classes, empty = all

	// When to trigger
	On       string   `yaml:"on"`       // start, end, duration (default: start)

	// Time-of-day window (24h format, e.g. "22:00"-"06:00" for overnight)
	After    string   `yaml:"after"`    // e.g. "22:00"
	Before   string   `yaml:"before"`   // e.g. "06:00"

	// Day-of-week filter
	Days     []string `yaml:"days"`     // mon, tue, wed, thu, fri, sat, sun

	// Session criteria
	MinDuration   string `yaml:"min_duration"`    // e.g. "30s", "5m"
	MaxDuration   string `yaml:"max_duration"`    // e.g. "1h"
	MinDetections int    `yaml:"min_detections"`  // minimum detection count
	MinConfidence float32 `yaml:"min_confidence"` // minimum peak confidence

	// Camera count: how many cameras must see the object
	// 0 = any, 1 = single camera, 2+ = multi-camera correlated
	MinCameras int `yaml:"min_cameras"`

	// Which backends to notify (empty = all configured)
	Backends []string `yaml:"backends"` // ntfy, webhook, mqtt

	// Override notification fields
	Priority string `yaml:"priority"` // override default priority
	Title    string `yaml:"title"`    // override title template
}

// Matches checks if a policy matches the given session and event type.
func (p *Policy) Matches(sess *events.Session, eventType string) bool {
	// Check event type (on)
	trigger := p.On
	if trigger == "" {
		trigger = "start"
	}

	switch trigger {
	case "start":
		if eventType != events.TypeSessionStart {
			return false
		}
	case "end":
		if eventType != events.TypeSessionEnd {
			return false
		}
	case "duration":
		// Matches on end, with duration criteria
		if eventType != events.TypeSessionEnd {
			return false
		}
	default:
		return false
	}

	// Check cameras
	if len(p.Cameras) > 0 && !containsAny(p.Cameras, sess.Cameras) {
		return false
	}

	// Check regions
	if len(p.Regions) > 0 && !contains(p.Regions, sess.Region) {
		return false
	}

	// Check classes
	if len(p.Classes) > 0 && !contains(p.Classes, sess.Class) {
		return false
	}

	// Check time-of-day
	if !p.matchesTimeOfDay(time.Now()) {
		return false
	}

	// Check day-of-week
	if !p.matchesDayOfWeek(time.Now()) {
		return false
	}

	// Check duration criteria
	if p.MinDuration != "" {
		minDur, _ := time.ParseDuration(p.MinDuration)
		if minDur > 0 && sess.Duration < minDur.Seconds() {
			return false
		}
	}
	if p.MaxDuration != "" {
		maxDur, _ := time.ParseDuration(p.MaxDuration)
		if maxDur > 0 && sess.Duration > maxDur.Seconds() {
			return false
		}
	}

	// Check detection count
	if p.MinDetections > 0 && sess.DetectionCount < p.MinDetections {
		return false
	}

	// Check confidence
	if p.MinConfidence > 0 && sess.PeakConfidence < p.MinConfidence {
		return false
	}

	// Check camera count
	if p.MinCameras > 0 && len(sess.Cameras) < p.MinCameras {
		return false
	}

	return true
}

func (p *Policy) matchesTimeOfDay(now time.Time) bool {
	if p.After == "" && p.Before == "" {
		return true
	}

	current := now.Hour()*60 + now.Minute()

	after := -1
	before := 24 * 60

	if p.After != "" {
		after = parseTimeOfDay(p.After)
	}
	if p.Before != "" {
		before = parseTimeOfDay(p.Before)
	}

	if after < 0 || before < 0 {
		return true // invalid config, allow
	}

	if after <= before {
		// Normal range: e.g. 08:00 - 18:00
		return current >= after && current < before
	}

	// Overnight range: e.g. 22:00 - 06:00
	return current >= after || current < before
}

func (p *Policy) matchesDayOfWeek(now time.Time) bool {
	if len(p.Days) == 0 {
		return true
	}

	dayNames := map[time.Weekday]string{
		time.Monday:    "mon",
		time.Tuesday:   "tue",
		time.Wednesday: "wed",
		time.Thursday:  "thu",
		time.Friday:    "fri",
		time.Saturday:  "sat",
		time.Sunday:    "sun",
	}

	today := dayNames[now.Weekday()]
	return contains(p.Days, today)
}

func parseTimeOfDay(s string) int {
	var h, m int
	n, _ := fmt.Sscanf(s, "%d:%d", &h, &m)
	if n < 1 {
		return -1
	}
	return h*60 + m
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func containsAny(list []string, candidates []string) bool {
	for _, c := range candidates {
		if contains(list, c) {
			return true
		}
	}
	return false
}
