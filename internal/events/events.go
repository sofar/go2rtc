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

// recentEvents stores the last N events for the query API.
var recentEvents []*Event
var recentMu sync.RWMutex

const maxRecentEvents = 1000

func Init() {
	log = app.GetLogger("events")

	Default = NewBus()

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

	// REST API for querying recent events
	api.HandleFunc("api/events", apiEvents)

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
