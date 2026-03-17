// Package offline tracks camera connection state and serves placeholder
// JPEG snapshots for cameras that are currently disconnected.
//
// It hooks into the streams.Producer lifecycle via the Listener/Fire
// pattern: when a producer enters reconnect (offline) or successfully
// reconnects (online), it fires a ProducerStateChange event that this
// module observes.
package offline

import (
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// state tracks the online/offline status of a camera stream.
type state struct {
	Online    bool      `json:"online"`
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error,omitempty"`

	// cached placeholder JPEG, generated on first offline request
	placeholder []byte
}

var states = map[string]*state{}
var statesMu sync.RWMutex

func Init() {
	log = app.GetLogger("offline")

	// Initialize state for all registered cameras as online.
	for _, cam := range camera.All() {
		states[cam.Name] = &state{Online: true, Since: time.Now()}
	}

	// Subscribe to producer state changes on each camera's stream.
	for _, cam := range camera.All() {
		name := cam.Name
		stream := streams.Get(name)
		if stream == nil {
			continue
		}
		stream.Listen(func(msg any) {
			if sc, ok := msg.(streams.ProducerStateChange); ok {
				handleStateChange(name, sc)
			}
		})
	}

	api.HandleFunc("api/cameras/status", apiStatus)
	api.HandleFunc("api/cameras/snapshot", apiSnapshot)
}

func handleStateChange(name string, sc streams.ProducerStateChange) {
	statesMu.Lock()
	defer statesMu.Unlock()

	s, ok := states[name]
	if !ok {
		s = &state{}
		states[name] = s
	}

	if sc.Retry < 0 {
		// Reconnect succeeded
		if !s.Online {
			log.Info().Str("camera", name).Msg("[offline] camera back online")
			s.Online = true
			s.Since = time.Now()
			s.LastError = ""
			s.placeholder = nil // invalidate cached placeholder
		}
	} else {
		// Producer failed to connect
		if s.Online {
			log.Warn().Str("camera", name).Err(sc.Err).Msg("[offline] camera went offline")
		}
		s.Online = false
		s.Since = time.Now()
		if sc.Err != nil {
			s.LastError = sc.Err.Error()
		}
		s.placeholder = nil // regenerate with new error text
	}
}

// IsOnline returns whether a camera is currently online.
func IsOnline(name string) bool {
	statesMu.RLock()
	defer statesMu.RUnlock()
	if s, ok := states[name]; ok {
		return s.Online
	}
	return true // unknown cameras assumed online
}

// GetPlaceholder returns a JPEG placeholder for an offline camera.
// Returns nil if the camera is online or unknown.
func GetPlaceholder(name string) []byte {
	statesMu.Lock()
	defer statesMu.Unlock()

	s, ok := states[name]
	if !ok || s.Online {
		return nil
	}

	if s.placeholder == nil {
		reason := s.LastError
		if reason == "" {
			reason = "connection lost"
		}
		s.placeholder = RenderPlaceholder(name, reason)
	}

	return s.placeholder
}

// apiStatus returns the online/offline state of all cameras.
func apiStatus(w http.ResponseWriter, r *http.Request) {
	if name := r.URL.Query().Get("name"); name != "" {
		statesMu.RLock()
		s, ok := states[name]
		statesMu.RUnlock()
		if !ok {
			http.Error(w, "camera not found: "+name, http.StatusNotFound)
			return
		}
		api.ResponseJSON(w, s)
		return
	}

	statesMu.RLock()
	defer statesMu.RUnlock()
	api.ResponseJSON(w, states)
}

// apiSnapshot serves a placeholder JPEG for an offline camera, or
// redirects to the normal snapshot endpoint if the camera is online.
func apiSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name parameter required", http.StatusBadRequest)
		return
	}

	placeholder := GetPlaceholder(name)
	if placeholder == nil {
		// Camera is online — redirect to the normal snapshot endpoint
		http.Redirect(w, r, "/api/frame.jpeg?src="+name, http.StatusTemporaryRedirect)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(placeholder)
}
