package events

import (
	"fmt"
	"sync"
	"time"
)

// Session event types.
const (
	TypeSessionStart  = "session_start"
	TypeSessionEnd    = "session_end"
)

// Session represents a detection session — a continuous period during
// which a particular object class is detected in a region. Sessions
// start when a class first appears, remain active while detections
// keep arriving, and end after a configurable quiet period.
type Session struct {
	ID             string    `json:"id"`
	Camera         string    `json:"camera"`
	Cameras        []string  `json:"cameras"`          // all cameras that contributed
	Region         string    `json:"region"`
	Class          string    `json:"class"`
	Start          time.Time `json:"start"`
	LastSeen       time.Time `json:"last_seen"`
	End            time.Time `json:"end,omitempty"`
	Duration       float64   `json:"duration"`         // seconds
	PeakConfidence float32   `json:"peak_confidence"`
	DetectionCount int       `json:"detection_count"`
	Active         bool      `json:"active"`

	camerasSet map[string]bool `json:"-"` // internal dedup
}

// sessionKey uniquely identifies a session by region + class.
// Camera is excluded so the same region name across cameras
// merges into one session.
type sessionKey struct {
	region string
	class  string
}

// DetectionDetail is the minimal info the tracker needs from a
// detection event. The inference module provides this via
// RegisterDetectionParser.
type DetectionDetail struct {
	Region     string
	Class      string
	Confidence float32
}

// DetectionParser extracts detection details from an event's Data field.
// Returns nil if the data is not a detection payload.
type DetectionParser func(data any) []DetectionDetail

var detectionParser DetectionParser

// RegisterDetectionParser is called by the inference module to teach
// the tracker how to read detection events without a circular import.
func RegisterDetectionParser(fn DetectionParser) {
	detectionParser = fn
}

// pendingSession tracks detections that haven't yet met the min_frames
// threshold to become a full session.
type pendingSession struct {
	camera    string
	region    string
	class     string
	count     int
	firstSeen time.Time
	lastSeen  time.Time
	peakConf  float32
}

// Tracker aggregates raw detection events into sessions.
type Tracker struct {
	bus       *Bus
	quietDur  time.Duration
	minFrames int // detections required before session starts

	mu      sync.Mutex
	active  map[sessionKey]*Session
	pending map[sessionKey]*pendingSession
	ended   []*Session
	seqID   int

	stop chan struct{}
}

const (
	defaultQuietDuration = 5 * time.Second
	maxEndedSessions     = 500
)

// NewTracker creates a session tracker. minFrames is the number of
// consecutive detections required before a session starts (0 or 1 = immediate).
func NewTracker(bus *Bus, quietDur time.Duration, minFrames int) *Tracker {
	if quietDur <= 0 {
		quietDur = defaultQuietDuration
	}
	if minFrames < 1 {
		minFrames = 1
	}
	return &Tracker{
		bus:       bus,
		quietDur:  quietDur,
		minFrames: minFrames,
		active:    make(map[sessionKey]*Session),
		pending:   make(map[sessionKey]*pendingSession),
		stop:      make(chan struct{}),
	}
}

// Start begins tracking sessions.
func (t *Tracker) Start() {
	ch := t.bus.Subscribe(Filter{Type: TypeDetection}, 256)

	go func() {
		for {
			select {
			case <-t.stop:
				t.bus.Unsubscribe(ch)
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				t.handleDetection(e)
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-ticker.C:
				t.expireSessions()
			}
		}
	}()
}

// Stop terminates the tracker.
func (t *Tracker) Stop() {
	close(t.stop)
}

// ActiveSessions returns a snapshot of all currently active sessions.
func (t *Tracker) ActiveSessions() []*Session {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	result := make([]*Session, 0, len(t.active))
	for _, s := range t.active {
		cp := *s
		cp.Duration = now.Sub(s.Start).Seconds()
		result = append(result, &cp)
	}
	return result
}

// RecentSessions returns recently ended sessions.
func (t *Tracker) RecentSessions() []*Session {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]*Session, len(t.ended))
	copy(result, t.ended)
	return result
}

func (t *Tracker) handleDetection(e *Event) {
	if detectionParser == nil {
		return
	}

	details := detectionParser(e.Data)
	if len(details) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	for _, det := range details {
		key := sessionKey{region: det.Region, class: det.Class}

		// Already an active session — extend it
		if s, exists := t.active[key]; exists {
			s.LastSeen = now
			s.DetectionCount++
			if det.Confidence > s.PeakConfidence {
				s.PeakConfidence = det.Confidence
			}
			s.Camera = e.Camera
			if !s.camerasSet[e.Camera] {
				s.camerasSet[e.Camera] = true
				s.Cameras = append(s.Cameras, e.Camera)
			}
			continue
		}

		// Check pending (pre-session accumulator)
		if p, exists := t.pending[key]; exists {
			p.count++
			p.lastSeen = now
			p.camera = e.Camera
			if det.Confidence > p.peakConf {
				p.peakConf = det.Confidence
			}

			if p.count >= t.minFrames {
				// Promote to active session
				delete(t.pending, key)
				t.seqID++
				s := &Session{
					ID:             fmt.Sprintf("%s-%d", now.Format("20060102-150405"), t.seqID),
					Camera:         e.Camera,
					Cameras:        []string{e.Camera},
					Region:         det.Region,
					Class:          det.Class,
					Start:          p.firstSeen,
					LastSeen:       now,
					PeakConfidence: p.peakConf,
					DetectionCount: p.count,
					Active:         true,
					camerasSet:     map[string]bool{e.Camera: true},
				}
				t.active[key] = s
				t.bus.Publish(&Event{
					Type:   TypeSessionStart,
					Camera: e.Camera,
					Data:   sessionCopy(s, now),
				})
			}
			continue
		}

		// New detection — start pending
		t.pending[key] = &pendingSession{
			camera:    e.Camera,
			region:    det.Region,
			class:     det.Class,
			count:     1,
			firstSeen: now,
			lastSeen:  now,
			peakConf:  det.Confidence,
		}

		// If minFrames is 1, promote immediately
		if t.minFrames <= 1 {
			p := t.pending[key]
			delete(t.pending, key)
			t.seqID++
			s := &Session{
				ID:             fmt.Sprintf("%s-%d", now.Format("20060102-150405"), t.seqID),
				Camera:         e.Camera,
				Cameras:        []string{e.Camera},
				Region:         det.Region,
				Class:          det.Class,
				Start:          now,
				LastSeen:       now,
				PeakConfidence: p.peakConf,
				DetectionCount: 1,
				Active:         true,
				camerasSet:     map[string]bool{e.Camera: true},
			}
			t.active[key] = s
			t.bus.Publish(&Event{
				Type:   TypeSessionStart,
				Camera: e.Camera,
				Data:   sessionCopy(s, now),
			})
		}
	}
}

func (t *Tracker) expireSessions() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for key, s := range t.active {
		if now.Sub(s.LastSeen) >= t.quietDur {
			s.End = now
			s.Duration = s.End.Sub(s.Start).Seconds()
			s.Active = false

			t.ended = append(t.ended, sessionCopy(s, now))
			if len(t.ended) > maxEndedSessions {
				t.ended = t.ended[len(t.ended)-maxEndedSessions:]
			}

			t.bus.Publish(&Event{
				Type:   TypeSessionEnd,
				Camera: s.Camera,
				Data:   sessionCopy(s, now),
			})

			delete(t.active, key)
		}
	}

	// Expire stale pending detections that never reached minFrames
	for key, p := range t.pending {
		if now.Sub(p.lastSeen) >= t.quietDur {
			delete(t.pending, key)
		}
	}
}

func sessionCopy(s *Session, now time.Time) *Session {
	cp := *s
	if cp.Active {
		cp.Duration = now.Sub(s.Start).Seconds()
	}
	return &cp
}
