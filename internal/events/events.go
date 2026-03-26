package events

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// DefaultSnapshots is the global session snapshot store.
var DefaultSnapshots *SnapshotStore

// SessionBasePath is where session data is stored on disk.
var SessionBasePath string

// recentEvents stores the last N events for the query API.
var recentEvents []*Event
var recentMu sync.RWMutex

const maxRecentEvents = 1000

func Init() {
	log = app.GetLogger("events")

	var cfg struct {
		Storage struct {
			BasePath string `yaml:"base_path"`
		} `yaml:"storage"`
	}
	app.LoadConfig(&cfg)

	Default = NewBus()

	// Start session tracker (aggregates detections into sessions).
	// minFrames is configured later by inference.Init via SetMinFrames.
	DefaultTracker = NewTracker(Default, defaultQuietDuration, 1)

	// Restore sessions from disk before starting
	if cfg.Storage.BasePath != "" {
		if err := DefaultTracker.LoadState(cfg.Storage.BasePath); err != nil {
			log.Error().Err(err).Msg("[events] load sessions")
		}
	}

	DefaultTracker.Start()

	// Start periodic session persistence
	if cfg.Storage.BasePath != "" {
		DefaultTracker.startPeriodicSave(cfg.Storage.BasePath)
	}

	// Start snapshot store if storage is configured
	if cfg.Storage.BasePath != "" {
		SessionBasePath = cfg.Storage.BasePath
		DefaultSnapshots = NewSnapshotStore(cfg.Storage.BasePath, 5*time.Second, Default, DefaultTracker)
		DefaultSnapshots.Start()
		log.Info().Str("path", cfg.Storage.BasePath).Msg("[events] session snapshots enabled")
	}

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
	api.HandleFunc("api/sessions/snapshots", apiSessionSnapshots)
	api.HandleFunc("api/sessions/snapshot/", apiSessionSnapshotFile)
	api.HandleFunc("api/sessions/stored", apiStoredSessions)
	api.HandleFunc("api/sessions/neighbors", apiSessionNeighbors)

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

// SetMinFrames configures how many consecutive detections are required
// before a session starts. Called by inference.Init after loading config.
func SetMinFrames(n int) {
	if DefaultTracker != nil && n > 0 {
		DefaultTracker.mu.Lock()
		DefaultTracker.minFrames = n
		DefaultTracker.mu.Unlock()
		log.Info().Int("min_frames", n).Msg("[events] session min_frames updated")
	}
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

// apiSessionSnapshots returns snapshot list for a session by ID.
func apiSessionSnapshots(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}

	if SessionBasePath == "" {
		http.Error(w, "storage not configured", http.StatusNotFound)
		return
	}

	stored, err := GetStoredSession(SessionBasePath, id)
	if err != nil {
		http.Error(w, "session not found: "+id, http.StatusNotFound)
		return
	}

	api.ResponseJSON(w, stored)
}

// apiSessionSnapshotFile serves a session snapshot JPEG file.
// Path: /api/sessions/snapshot/<session-id>/<filename>
func apiSessionSnapshotFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// Extract session-id/filename from path
	prefix := "/api/sessions/snapshot/"
	if len(path) <= len(prefix) {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	rest := path[len(prefix):]

	if SessionBasePath == "" {
		http.Error(w, "storage not configured", http.StatusNotFound)
		return
	}

	fullPath := filepath.Join(SessionBasePath, "sessions", rest)

	// Security: ensure the resolved path is under basePath
	abs, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	base, _ := filepath.Abs(SessionBasePath)
	if len(abs) < len(base) || abs[:len(base)] != base {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, fullPath)
}

// apiSessionNeighbors returns the prev/next session IDs for navigation.
// When region is specified, only sessions matching that region are considered.
func apiSessionNeighbors(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}
	region := r.URL.Query().Get("region")
	cameraFilter := r.URL.Query().Get("camera")

	if SessionBasePath == "" {
		api.ResponseJSON(w, map[string]any{"prev": nil, "next": nil})
		return
	}

	sessDir := filepath.Join(SessionBasePath, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		api.ResponseJSON(w, map[string]any{"prev": nil, "next": nil})
		return
	}

	// matchesFilter checks if a session directory matches the region/camera filter.
	// Only reads session.json when a filter is active.
	matchesFilter := func(name string) bool {
		if region == "" && cameraFilter == "" {
			return true
		}
		stored, err := GetStoredSession(SessionBasePath, name)
		if err != nil || stored.Session == nil {
			return false
		}
		if region != "" && stored.Session.Region != region {
			return false
		}
		if cameraFilter != "" && stored.Session.Camera != cameraFilter {
			return false
		}
		return true
	}

	// entries are sorted by name (ascending) = chronological order
	var prev, next *string
	for i, entry := range entries {
		if !entry.IsDir() || entry.Name() != id {
			continue
		}
		// Look backwards for prev
		for j := i - 1; j >= 0; j-- {
			if entries[j].IsDir() && matchesFilter(entries[j].Name()) {
				s := entries[j].Name()
				prev = &s
				break
			}
		}
		// Look forwards for next
		for j := i + 1; j < len(entries); j++ {
			if entries[j].IsDir() && matchesFilter(entries[j].Name()) {
				s := entries[j].Name()
				next = &s
				break
			}
		}
		break
	}

	api.ResponseJSON(w, map[string]any{"prev": prev, "next": next})
}

// apiStoredSessions returns recently stored (completed) sessions from disk.
func apiStoredSessions(w http.ResponseWriter, r *http.Request) {
	if SessionBasePath == "" {
		api.ResponseJSON(w, []*StoredSession{})
		return
	}

	limit := 50
	sessions := ListStoredSessions(SessionBasePath, limit)
	if sessions == nil {
		sessions = []*StoredSession{}
	}
	api.ResponseJSON(w, sessions)
}
