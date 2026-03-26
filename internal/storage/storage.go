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
	"context"
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

// basePath is stored for remote download caching.
var globalBasePath string

// globalPattern is the configured path pattern for segment paths.
var globalPattern *PathPattern

// remotePattern is the upload module's path pattern. Set by upload.Init().
var RemotePattern *PathPattern

// RemoteSegmentInfo is a segment available on remote storage.
type RemoteSegmentInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Remote storage callbacks — set by the upload module to avoid circular imports.
var (
	// RemoteDownload fetches a remote file to a local path.
	RemoteDownload func(ctx context.Context, remotePath, localPath string) error
	// RemoteList lists files under a remote prefix.
	RemoteList func(ctx context.Context, prefix string) ([]RemoteSegmentInfo, error)
)

func Init() {
	log = app.GetLogger("storage")

	var cfg struct {
		Mod Config `yaml:"storage"`
	}
	app.LoadConfig(&cfg)

	if cfg.Mod.BasePath == "" {
		return // storage not configured
	}
	globalBasePath = cfg.Mod.BasePath
	globalPattern = NewPathPattern(cfg.Mod.PathPattern)

	log.Info().Str("path_pattern", globalPattern.String()).Msg("[storage] path pattern")

	// "retention" is preferred; "default_retention" is a backward-compat alias
	retentionStr := cfg.Mod.Retention
	if retentionStr == "" {
		retentionStr = cfg.Mod.DefaultRetention
	}
	retention, err := ParseRetention(retentionStr)
	if err != nil {
		log.Error().Err(err).Msg("[storage] invalid retention")
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

		recorder := NewRecorder(cam.Name, basePath, globalPattern, segDur, cfg.Mod.Format)
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

	// Start local retention cleanup
	if !retention.IsZero() {
		runRetention(cfg.Mod.BasePath, retention, time.Hour)
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

	// Merge remote segments that aren't available locally
	remote := listRemoteSegments(cameraName, from, to)
	segments = mergeSegments(segments, remote)

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

	// Find the segment file (could be .mp4, .ts, or .mkv)
	var path string
	for _, ext := range []string{".mp4", ".ts", ".mkv"} {
		candidate := globalPattern.FormatFull(rec.basePath, cameraName, t, ext)
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}

	// If not found locally, try to fetch from remote backend
	if path == "" {
		path = fetchFromRemote(rec.basePath, cameraName, t)
	}
	if path == "" {
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	// Set content type based on extension
	switch filepath.Ext(path) {
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".mkv":
		w.Header().Set("Content-Type", "video/x-matroska")
	default:
		w.Header().Set("Content-Type", "video/mp4")
	}
	http.ServeFile(w, r, path)
}

// listSegments scans the filesystem for segment files within a time range.
func listSegments(basePath, cameraName string, from, to time.Time) []Segment {
	var segments []Segment

	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".mp4" && ext != ".ts" && ext != ".mkv" {
			return nil
		}

		rel, _ := filepath.Rel(basePath, path)
		cam, t, ok := globalPattern.Parse(rel)
		if !ok || cam != cameraName {
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

// fetchFromRemote attempts to download a segment from the remote backend.
// Returns the local path on success, or empty string on failure.
func fetchFromRemote(basePath, cameraName string, t time.Time) string {
	if RemoteDownload == nil || RemotePattern == nil {
		return ""
	}

	for _, ext := range []string{".mp4", ".ts", ".mkv"} {
		remotePath := RemotePattern.Format(cameraName, t) + ext
		localPath := globalPattern.FormatFull(basePath, cameraName, t, ext)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := RemoteDownload(ctx, remotePath, localPath)
		cancel()

		if err == nil {
			log.Debug().Str("path", remotePath).Msg("[storage] fetched from remote")
			return localPath
		}
	}
	return ""
}

// listRemoteSegments queries the upload backend for segments not available locally.
func listRemoteSegments(cameraName string, from, to time.Time) []Segment {
	if RemoteList == nil || RemotePattern == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use the remote pattern to determine the listing prefix.
	// We list from the root and filter by camera using the pattern parser.
	remoteSegs, err := RemoteList(ctx, "")
	if err != nil || len(remoteSegs) == 0 {
		return nil
	}

	var segments []Segment
	for _, rs := range remoteSegs {
		cam, t, ok := RemotePattern.Parse(rs.Path)
		if !ok || cam != cameraName {
			continue
		}
		if (!from.IsZero() && t.Before(from)) || t.After(to) {
			continue
		}

		segments = append(segments, Segment{
			Camera: cameraName,
			Start:  t,
			Size:   rs.Size,
			Path:   rs.Path,
			Remote: true,
		})
	}
	return segments
}

// mergeSegments combines local and remote segments, deduplicating by start time.
// Local segments take priority over remote ones.
func mergeSegments(local, remote []Segment) []Segment {
	if len(remote) == 0 {
		return local
	}

	// Build set of local segment times for dedup
	localTimes := make(map[int64]bool, len(local))
	for _, s := range local {
		localTimes[s.Start.Unix()] = true
	}

	// Add remote segments not present locally
	for _, s := range remote {
		if !localTimes[s.Start.Unix()] {
			local = append(local, s)
		}
	}

	sort.Slice(local, func(i, j int) bool {
		return local[i].Start.After(local[j].Start)
	})
	return local
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
