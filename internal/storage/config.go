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
	DefaultRetention string `yaml:"default_retention" json:"default_retention"` // deprecated: use retention
	Retention        string `yaml:"retention" json:"retention"`                 // local cache lifetime (e.g. "2d")
	SegmentDuration  string `yaml:"segment_duration" json:"segment_duration"`
	Format           string `yaml:"format" json:"format"` // mp4 (default), ts, mkv
}

// Segment represents a single recorded file on disk or remote storage.
type Segment struct {
	Camera    string    `json:"camera"`
	Start     time.Time `json:"start"`
	Duration  float64   `json:"duration"` // seconds
	Size      int64     `json:"size"`     // bytes
	Path      string    `json:"path"`
	VideoOnly bool      `json:"video_only,omitempty"`
	Remote    bool      `json:"remote,omitempty"` // true if only available on remote storage
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

// RetentionPolicy represents a retention limit — either time-based or size-based.
type RetentionPolicy struct {
	MaxAge  time.Duration // time-based: delete files older than this
	MaxSize int64         // size-based: delete oldest files when total exceeds this (bytes)
}

// IsZero returns true if no retention is configured.
func (r RetentionPolicy) IsZero() bool {
	return r.MaxAge == 0 && r.MaxSize == 0
}

// ParseRetention parses a retention string which can be:
//   - Duration: "5m", "24h", "14d"
//   - Size: "500M", "1G", "500000000" (plain bytes)
func ParseRetention(s string) (RetentionPolicy, error) {
	if s == "" {
		return RetentionPolicy{}, nil
	}

	// Try size first
	if size, err := parseSize(s); err == nil {
		return RetentionPolicy{MaxSize: size}, nil
	}

	// Try duration
	d, err := parseDuration(s)
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("invalid retention %q: not a duration or size", s)
	}
	return RetentionPolicy{MaxAge: d}, nil
}

// parseSize parses size strings like "500M", "1G", "2T", or plain byte counts.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	multiplier := int64(1)
	upper := strings.ToUpper(s)

	switch {
	case strings.HasSuffix(upper, "T"):
		multiplier = 1 << 40
		s = s[:len(s)-1]
	case strings.HasSuffix(upper, "G"):
		multiplier = 1 << 30
		s = s[:len(s)-1]
	case strings.HasSuffix(upper, "M"):
		multiplier = 1 << 20
		s = s[:len(s)-1]
	case strings.HasSuffix(upper, "K"):
		multiplier = 1 << 10
		s = s[:len(s)-1]
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * multiplier, nil
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
