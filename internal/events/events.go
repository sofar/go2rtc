package events

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Default is the global event bus instance.
var Default *Bus

// DefaultTracker is the global session tracker.
var DefaultTracker *Tracker

// recentEvents stores the last N events for the query API.
var recentEvents []*Event
var recentMu sync.RWMutex

const maxRecentEvents = 1000

func Init() {
	log = app.GetLogger("events")

	Default = NewBus()

	// Start session tracker (aggregates detections into sessions)
	DefaultTracker = NewTracker(Default, defaultQuietDuration)
	DefaultTracker.Start()

	// Store recent events for query API
	recent := Default.Subscribe(Filter{}, 256)
	go func() {
		for e := range recent {
			recentMu.Lock()
			recentEvents = append(recentEvents, e)
			if len(recentEvents) > maxRecentEvents {
				recentEvents = recentEvents[len(recentEvents)-maxRecentEvents:]
			}
			recentMu.Unlock()
		}
	}()

	// WebSocket live event stream
	ws.HandleFunc("events", wsHandler)

	// REST API
	api.HandleFunc("api/events", apiEvents)
	api.HandleFunc("api/sessions", apiSessions)

	log.Debug().Msg("[events] initialized")
}

// wsHandler streams events to WebSocket clients. The client sends a
// message with type "events" and an optional filter as value. Events
// are pushed to the client until the WebSocket closes.
func wsHandler(tr *ws.Transport, msg *ws.Message) error {
	var filter Filter
	if msg.Value != nil {
		_ = msg.Unmarshal(&filter)
	}

	ch := Default.Subscribe(filter, 64)

	// Push events to the WebSocket client
	go func() {
		for e := range ch {
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			tr.Write(&ws.Message{Type: "event", Value: json.RawMessage(b)})
		}
	}()

	tr.OnClose(func() {
		Default.Unsubscribe(ch)
	})

	return nil
}

// apiEvents returns recent events, optionally filtered by camera and time.
func apiEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cameraFilter := q.Get("camera")
	typeFilter := q.Get("type")
	fromStr := q.Get("from")
	toStr := q.Get("to")

	var from, to time.Time
	if fromStr != "" {
		from, _ = time.Parse(time.RFC3339, fromStr)
	}
	if toStr != "" {
		to, _ = time.Parse(time.RFC3339, toStr)
	}
	if to.IsZero() {
		to = time.Now()
	}

	recentMu.RLock()
	defer recentMu.RUnlock()

	var result []*Event
	for _, e := range recentEvents {
		if cameraFilter != "" && e.Camera != cameraFilter {
			continue
		}
		if typeFilter != "" && e.Type != typeFilter {
			continue
		}
		if !from.IsZero() && e.Timestamp.Before(from) {
			continue
		}
		if e.Timestamp.After(to) {
			continue
		}
		result = append(result, e)
	}

	if result == nil {
		result = []*Event{}
	}
	api.ResponseJSON(w, result)
}

// GetRecentByCamera returns the most recent event matching a type and camera
// from the stored event buffer. Returns nil if none found within maxAge.
func GetRecentByCamera(eventType, cameraName string, maxAge time.Duration) *Event {
	recentMu.RLock()
	defer recentMu.RUnlock()

	cutoff := time.Now().Add(-maxAge)
	// Search backwards (newest first)
	for i := len(recentEvents) - 1; i >= 0; i-- {
		e := recentEvents[i]
		if e.Timestamp.Before(cutoff) {
			break // events are chronological, no point searching further
		}
		if e.Type == eventType && e.Camera == cameraName {
			return e
		}
	}
	return nil
}

// apiSessions returns active and recently ended sessions.
func apiSessions(w http.ResponseWriter, r *http.Request) {
	if DefaultTracker == nil {
		api.ResponseJSON(w, map[string]any{"active": []*Session{}, "recent": []*Session{}})
		return
	}

	api.ResponseJSON(w, map[string]any{
		"active": DefaultTracker.ActiveSessions(),
		"recent": DefaultTracker.RecentSessions(),
	})
}
