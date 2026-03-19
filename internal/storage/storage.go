// Package storage provides segmented recording for camera streams.
//
// For each camera with recording enabled, a Recorder consumer is attached
// to the camera's stream. The recorder writes MP4 segments to disk using
// go2rtc's native MP4 muxer (no ffmpeg dependency).
//
// Segments are stored as:
//
//	<base_path>/<camera_name>/YYYY-MM-DD/HH-MM-SS.mp4
//
// A retention manager periodically removes segments older than the
// configured retention period.
package storage

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// recorders tracks active recorders by camera name.
var recorders = map[string]*Recorder{}
var recordersMu sync.RWMutex

func Init() {
	log = app.GetLogger("storage")

	var cfg struct {
		Mod Config `yaml:"storage"`
	}
	app.LoadConfig(&cfg)

	if cfg.Mod.BasePath == "" {
		return // storage not configured
	}

	defaultRetention, err := parseDuration(cfg.Mod.DefaultRetention)
	if err != nil {
		log.Error().Err(err).Msg("[storage] invalid default_retention")
		return
	}

	segDur, err := parseDuration(cfg.Mod.SegmentDuration)
	if err != nil {
		log.Error().Err(err).Msg("[storage] invalid segment_duration")
		return
	}

	// Start recording for each camera that has recording enabled
	for _, cam := range camera.All() {
		rec := cam.Recording
		if rec == nil || !rec.Enabled {
			continue
		}

		basePath := cfg.Mod.BasePath
		if rec.Path != "" {
			basePath = rec.Path
		}

		// Determine which stream to record from.
		// If transcoding is requested (codec is set), create a virtual
		// transcoded stream and record from that. Otherwise record the
		// raw camera stream directly.
		streamName := cam.Name
		if rec.Codec != "" {
			streamName = cam.Name + "/_recording"
			source := buildRecordingSource(cam.Name, rec)
			if _, err := streams.New(streamName, source); err != nil {
				log.Error().Err(err).Str("camera", cam.Name).Msg("[storage] create transcode stream")
				continue
			}
			log.Info().
				Str("camera", cam.Name).
				Str("source", source).
				Msg("[storage] transcode-on-record")
		}

		stream := streams.Get(streamName)
		if stream == nil {
			log.Error().Str("camera", cam.Name).Msg("[storage] stream not found")
			continue
		}

		recorder := NewRecorder(cam.Name, basePath, segDur)
		if err := stream.AddConsumer(recorder); err != nil {
			log.Error().Err(err).Str("camera", cam.Name).Msg("[storage] add consumer")
			continue
		}

		recordersMu.Lock()
		recorders[cam.Name] = recorder
		recordersMu.Unlock()

		log.Info().
			Str("camera", cam.Name).
			Str("path", basePath).
			Str("segment", cfg.Mod.SegmentDuration).
			Msg("[storage] recording started")
	}

	// Start retention cleanup
	if defaultRetention > 0 {
		runRetention(cfg.Mod.BasePath, defaultRetention, time.Hour)
	}

	api.HandleFunc("api/recordings", apiRecordings)
	api.HandleFunc("api/recordings/play", apiPlay)
}

// apiRecordings lists recorded segments for a camera within a time range.
func apiRecordings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cameraName := q.Get("camera")
	if cameraName == "" {
		http.Error(w, "camera parameter required", http.StatusBadRequest)
		return
	}

	recordersMu.RLock()
	rec, ok := recorders[cameraName]
	recordersMu.RUnlock()

	if !ok {
		http.Error(w, "no recording for camera: "+cameraName, http.StatusNotFound)
		return
	}

	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if to.IsZero() {
		to = time.Now()
	}

	segments := listSegments(rec.basePath, cameraName, from, to)
	api.ResponseJSON(w, segments)
}

// apiPlay serves a recorded segment file.
func apiPlay(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cameraName := q.Get("camera")
	ts := q.Get("ts")

	if cameraName == "" || ts == "" {
		http.Error(w, "camera and ts parameters required", http.StatusBadRequest)
		return
	}

	recordersMu.RLock()
	rec, ok := recorders[cameraName]
	recordersMu.RUnlock()

	if !ok {
		http.Error(w, "no recording for camera: "+cameraName, http.StatusNotFound)
		return
	}

	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		http.Error(w, "invalid ts: "+err.Error(), http.StatusBadRequest)
		return
	}

	path := segmentPath(rec.basePath, cameraName, t)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, path)
}

// listSegments scans the filesystem for segment files within a time range.
func listSegments(basePath, cameraName string, from, to time.Time) []Segment {
	var segments []Segment

	cameraDir := filepath.Join(basePath, cameraName)
	_ = filepath.Walk(cameraDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".mp4") {
			return nil
		}

		// Parse time from directory structure: YYYY-MM-DD/HH-MM-SS.mp4
		rel, _ := filepath.Rel(cameraDir, path)
		t := parseSegmentTime(rel)
		if t.IsZero() {
			return nil
		}

		if (!from.IsZero() && t.Before(from)) || t.After(to) {
			return nil
		}

		segments = append(segments, Segment{
			Camera: cameraName,
			Start:  t,
			Size:   info.Size(),
			Path:   rel,
		})
		return nil
	})

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Start.After(segments[j].Start)
	})

	return segments
}

// parseSegmentTime extracts a time from a path like "2024-01-15/14-30-00.mp4".
func parseSegmentTime(rel string) time.Time {
	// Expected format: YYYY-MM-DD/HH-MM-SS.mp4
	rel = strings.TrimSuffix(rel, ".mp4")
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 {
		return time.Time{}
	}

	t, err := time.ParseInLocation("2006-01-02/15-04-05", parts[0]+"/"+parts[1], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// buildRecordingSource creates an ffmpeg: source URL for transcode-on-record.
// Example output: ffmpeg:front_porch#video=h264#width=1920#height=1080#hardware=vaapi
func buildRecordingSource(cameraName string, rec *camera.RecordingConfig) string {
	source := "ffmpeg:" + cameraName + "#video=" + rec.Codec

	if rec.Resolution != "" {
		for _, sep := range []string{"x", "X", ":"} {
			parts := strings.SplitN(rec.Resolution, sep, 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				source += "#width=" + parts[0] + "#height=" + parts[1]
				break
			}
		}
	}

	if rec.Bitrate != "" {
		source += "#bitrate=" + rec.Bitrate
	}

	if rec.Hardware != "" {
		source += "#hardware=" + rec.Hardware
	}

	return source
}

// GetRecorder returns the active recorder for a camera, or nil.
func GetRecorder(name string) *Recorder {
	recordersMu.RLock()
	defer recordersMu.RUnlock()
	return recorders[name]
}

// RecorderStatus returns a JSON-serializable summary of all recorders.
func RecorderStatus() map[string]any {
	recordersMu.RLock()
	defer recordersMu.RUnlock()

	result := make(map[string]any, len(recorders))
	for name, rec := range recorders {
		b, _ := json.Marshal(rec)
		var m map[string]any
		json.Unmarshal(b, &m)
		m["segments"] = rec.Segments
		m["bytes"] = rec.Bytes
		result[name] = m
	}
	return result
}
