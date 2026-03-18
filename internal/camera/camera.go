// Package camera extends go2rtc's flat streams config with camera-specific
// metadata: regions of interest, view group membership, recording settings,
// and transcode profiles.
//
// On Init(), each camera is registered as a go2rtc stream (via streams.New)
// and exposed through the api/cameras endpoint.
package camera

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/AlexxIT/go2rtc/pkg/yaml"
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
	api.HandleFunc("api/cameras/regions", apiRegions)
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

	// Register transcode profiles as virtual streams:
	//   front_porch/low  → ffmpeg:front_porch#video=h264#width=640#height=360
	//   front_porch/high → ffmpeg:front_porch#video=h264#width=1920#height=1080
	for profileName, profile := range cam.Transcode {
		source := buildTranscodeSource(cam.Name, profile)
		streamName := cam.Name + "/" + profileName
		if _, err := streams.New(streamName, source); err != nil {
			log.Warn().Err(err).
				Str("camera", cam.Name).
				Str("profile", profileName).
				Msg("[camera] register transcode profile")
			continue
		}
		log.Info().
			Str("stream", streamName).
			Str("source", source).
			Msg("[camera] transcode profile registered")
	}

	camerasMu.Lock()
	cameras[cam.Name] = cam
	camerasMu.Unlock()

	return nil
}

// buildTranscodeSource creates an ffmpeg: source URL for a transcode profile.
func buildTranscodeSource(cameraName string, profile *Transcode) string {
	source := "ffmpeg:" + cameraName + "#video=" + profile.Codec

	if profile.Resolution != "" {
		// Resolution format: "640x360" or "1920x1080"
		parts := splitResolution(profile.Resolution)
		if len(parts) == 2 {
			source += "#width=" + parts[0] + "#height=" + parts[1]
		}
	}

	return source
}

// splitResolution parses "WIDTHxHEIGHT" into ["WIDTH", "HEIGHT"].
func splitResolution(s string) []string {
	for _, sep := range []string{"x", "X", ":"} {
		if i := len(sep); i > 0 {
			parts := strings.SplitN(s, sep, 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				return parts
			}
		}
	}
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

// apiRegions handles PUT /api/cameras/regions?name=X to update a
// camera's regions live and persist to the config file.
func apiRegions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(w, "PUT required", http.StatusMethodNotAllowed)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name parameter required", http.StatusBadRequest)
		return
	}

	cam := Get(name)
	if cam == nil {
		http.Error(w, "camera not found: "+name, http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var regions map[string]*Region
	if err := json.Unmarshal(body, &regions); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate
	for rname, region := range regions {
		if len(region.Polygon) < 3 {
			http.Error(w, fmt.Sprintf("region %q needs at least 3 points", rname), http.StatusBadRequest)
			return
		}
	}

	// Update in-memory
	camerasMu.Lock()
	cam.Regions = regions
	camerasMu.Unlock()

	// Persist to config file
	if err := persistRegions(name, regions); err != nil {
		log.Error().Err(err).Str("camera", name).Msg("[camera] persist regions")
		http.Error(w, "saved in memory but failed to write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Info().Str("camera", name).Int("regions", len(regions)).Msg("[camera] regions updated")
	api.ResponseJSON(w, regions)
}

// persistRegions updates the regions for a camera in the YAML config
// file using yaml.Patch to preserve formatting and comments.
func persistRegions(cameraName string, regions map[string]*Region) error {
	if app.ConfigPath == "" {
		return fmt.Errorf("no config file path")
	}

	data, err := os.ReadFile(app.ConfigPath)
	if err != nil {
		return err
	}

	// Convert regions to a generic map for YAML serialization
	regionsMap := make(map[string]any, len(regions))
	for name, region := range regions {
		regionsMap[name] = map[string]any{
			"polygon": region.Polygon,
			"detect":  region.Detect,
		}
	}

	// Patch cameras.<cameraName>.regions in the YAML
	path := []string{"cameras", cameraName, "regions"}
	out, err := yaml.Patch(data, path, regionsMap)
	if err != nil {
		return err
	}

	return os.WriteFile(app.ConfigPath, out, 0o644)
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
