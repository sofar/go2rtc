package storage

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config holds the global storage configuration.
type Config struct {
	BasePath         string `yaml:"base_path" json:"base_path"`
	DefaultRetention string `yaml:"default_retention" json:"default_retention"`
	SegmentDuration  string `yaml:"segment_duration" json:"segment_duration"`
	Format           string `yaml:"format" json:"format"` // mp4 (default), ts, mkv
}

// Segment represents a single recorded file on disk.
type Segment struct {
	Camera    string    `json:"camera"`
	Start     time.Time `json:"start"`
	Duration  float64   `json:"duration"` // seconds
	Size      int64     `json:"size"`     // bytes
	Path      string    `json:"path"`
	VideoOnly bool      `json:"video_only,omitempty"`
}

// segmentDir returns the date directory for a timestamp.
// Layout: <base>/<camera>/YYYY-MM-DD/
func segmentDir(basePath, camera string, t time.Time) string {
	return fmt.Sprintf("%s/%s/%s", basePath, camera, t.Format("2006-01-02"))
}

// segmentFilename returns the filename for a segment.
// Layout: HH-MM-SS.mp4
func segmentFilename(t time.Time) string {
	return t.Format("15-04-05") + ".mp4"
}

// segmentPath returns the full path for a segment file.
func segmentPath(basePath, camera string, t time.Time) string {
	return segmentDir(basePath, camera, t) + "/" + segmentFilename(t)
}

// parseDuration parses durations like "5m", "30d", "24h", "14d".
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}

	// Handle day suffix which time.ParseDuration doesn't support
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, fmt.Errorf("storage: invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("storage: invalid duration %q", s)
	}
	return d, nil
}
