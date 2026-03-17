// Package camera extends go2rtc's flat streams config with camera-specific
// metadata: regions of interest, view group membership, recording settings,
// and transcode profiles.
//
// On Init(), each camera is registered as a go2rtc stream (via streams.New)
// and exposed through the api/cameras endpoint.
package camera

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// cameras is the global registry, keyed by camera name.
var cameras = map[string]*Camera{}
var camerasMu sync.RWMutex

func Init() {
	log = app.GetLogger("camera")

	cfg := loadConfig()
	if len(cfg) == 0 {
		return
	}

	for name, cam := range cfg {
		cam.Name = name
		if err := register(cam); err != nil {
			log.Error().Err(err).Str("camera", name).Msg("[camera] register")
			continue
		}
		log.Info().Str("camera", name).Str("stream", cam.Stream).Msg("[camera] registered")
	}

	api.HandleFunc("api/cameras", apiCameras)
}

// register validates a camera config and registers its stream.
func register(cam *Camera) error {
	if err := cam.validate(); err != nil {
		return err
	}

	// Register the camera's source as a go2rtc stream.
	if _, err := streams.New(cam.Name, cam.Stream); err != nil {
		return err
	}

	camerasMu.Lock()
	cameras[cam.Name] = cam
	camerasMu.Unlock()

	return nil
}

// Get returns a camera by name, or nil if not found.
func Get(name string) *Camera {
	camerasMu.RLock()
	defer camerasMu.RUnlock()
	return cameras[name]
}

// All returns all registered cameras sorted by name.
func All() []*Camera {
	camerasMu.RLock()
	defer camerasMu.RUnlock()

	result := make([]*Camera, 0, len(cameras))
	for _, cam := range cameras {
		result = append(result, cam)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// apiCameras handles GET /api/cameras and GET /api/cameras?name=X.
func apiCameras(w http.ResponseWriter, r *http.Request) {
	if name := r.URL.Query().Get("name"); name != "" {
		cam := Get(name)
		if cam == nil {
			http.Error(w, "camera not found: "+name, http.StatusNotFound)
			return
		}
		api.ResponseJSON(w, cam)
		return
	}

	api.ResponseJSON(w, All())
}

// loadConfig parses the cameras: section from go2rtc config.
func loadConfig() map[string]*Camera {
	var cfg struct {
		Mod map[string]*Camera `yaml:"cameras"`
	}
	app.LoadConfig(&cfg)

	// Also support inline stream-only shorthand:
	//   cameras:
	//     front: rtsp://...
	// This is handled by Camera's UnmarshalYAML.

	return cfg.Mod
}

// UnmarshalYAML allows cameras to be defined as either a string (stream
// URL only) or a full object with metadata.
func (c *Camera) UnmarshalYAML(unmarshal func(any) error) error {
	// Try string shorthand first: cameras: { name: "rtsp://..." }
	var s string
	if err := unmarshal(&s); err == nil {
		c.Stream = s
		return nil
	}

	// Full object form
	type plain Camera // avoid recursion
	return unmarshal((*plain)(c))
}

// MarshalJSON controls the API response format. Credentials are masked
// in the stream URL for safe exposure over the API.
func (c *Camera) MarshalJSON() ([]byte, error) {
	type plain Camera
	cp := (*plain)(c)

	// Mask credentials in stream URL
	masked := *cp
	masked.Stream = creds.SecretString(cp.Stream)
	return json.Marshal(&masked)
}
