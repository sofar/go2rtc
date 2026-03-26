package storage

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config holds the global storage configuration.
type Config struct {
	BasePath         string `yaml:"base_path" json:"base_path"`
	PathPattern      string `yaml:"path_pattern" json:"path_pattern"` // e.g. "{camera}/{year}-{month}-{day}/{hour}-{minute}-{second}"
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

// DefaultPathPattern is the default segment path layout.
const DefaultPathPattern = "{camera}/{year}-{month}-{day}/{hour}-{minute}-{second}"

// PathPattern formats and parses segment paths using variable substitution.
//
// Variables: {camera}, {year}, {month}, {day}, {hour}, {minute}, {second}
//
// Examples:
//
//	"{camera}/{year}-{month}-{day}/{hour}-{minute}-{second}"
//	→ "front_porch/2026-03-25/14-30-00.mp4"
//
//	"{year}/{month}/{day}/{camera}_{hour}{minute}{second}"
//	→ "2026/03/25/front_porch_143000.mp4"
//
//	"{camera}_{year}{month}{day}_{hour}{minute}{second}"
//	→ "front_porch_20260325_143000.mp4"
type PathPattern struct {
	pattern string
	re      *regexp.Regexp // compiled regex for parsing
}

// NewPathPattern creates a pattern from a template string.
func NewPathPattern(pattern string) *PathPattern {
	if pattern == "" {
		pattern = DefaultPathPattern
	}
	p := &PathPattern{pattern: pattern}
	p.compile()
	return p
}

// Format returns the relative path (without extension) for a camera and time.
func (p *PathPattern) Format(camera string, t time.Time) string {
	r := strings.NewReplacer(
		"{camera}", camera,
		"{year}", t.Format("2006"),
		"{month}", t.Format("01"),
		"{day}", t.Format("02"),
		"{hour}", t.Format("15"),
		"{minute}", t.Format("04"),
		"{second}", t.Format("05"),
	)
	return r.Replace(p.pattern)
}

// FormatFull returns the full local path including base path and extension.
func (p *PathPattern) FormatFull(basePath, camera string, t time.Time, ext string) string {
	return basePath + "/" + p.Format(camera, t) + ext
}

// FormatDir returns the directory portion of the formatted path.
func (p *PathPattern) FormatDir(basePath, camera string, t time.Time) string {
	full := p.FormatFull(basePath, camera, t, "")
	if idx := strings.LastIndex(full, "/"); idx >= 0 {
		return full[:idx]
	}
	return basePath
}

// Parse extracts camera name and timestamp from a relative path (with extension stripped).
// Returns zero values if the path doesn't match the pattern.
func (p *PathPattern) Parse(rel string) (camera string, t time.Time, ok bool) {
	// Strip extension
	for _, ext := range []string{".mp4", ".ts", ".mkv"} {
		if strings.HasSuffix(rel, ext) {
			rel = strings.TrimSuffix(rel, ext)
			break
		}
	}

	m := p.re.FindStringSubmatch(rel)
	if m == nil {
		return "", time.Time{}, false
	}

	names := p.re.SubexpNames()
	vals := map[string]string{}
	for i, name := range names {
		if name != "" {
			vals[name] = m[i]
		}
	}

	camera = vals["camera"]

	year, _ := strconv.Atoi(vals["year"])
	month, _ := strconv.Atoi(vals["month"])
	day, _ := strconv.Atoi(vals["day"])
	hour, _ := strconv.Atoi(vals["hour"])
	minute, _ := strconv.Atoi(vals["minute"])
	second, _ := strconv.Atoi(vals["second"])

	if year == 0 || month == 0 || day == 0 {
		return "", time.Time{}, false
	}

	t = time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
	return camera, t, true
}

// String returns the pattern template.
func (p *PathPattern) String() string { return p.pattern }

// compile builds a regex from the pattern for parsing paths.
func (p *PathPattern) compile() {
	// Escape regex metacharacters in the literal parts, then replace variables
	// with named capture groups.
	escaped := regexp.QuoteMeta(p.pattern)

	replacements := []struct{ from, to string }{
		{regexp.QuoteMeta("{camera}"), `(?P<camera>[^/]+)`},
		{regexp.QuoteMeta("{year}"), `(?P<year>\d{4})`},
		{regexp.QuoteMeta("{month}"), `(?P<month>\d{2})`},
		{regexp.QuoteMeta("{day}"), `(?P<day>\d{2})`},
		{regexp.QuoteMeta("{hour}"), `(?P<hour>\d{2})`},
		{regexp.QuoteMeta("{minute}"), `(?P<minute>\d{2})`},
		{regexp.QuoteMeta("{second}"), `(?P<second>\d{2})`},
	}

	for _, r := range replacements {
		escaped = strings.Replace(escaped, r.from, r.to, 1)
	}

	p.re = regexp.MustCompile("^" + escaped + "$")
}

// segmentDir returns the date directory for a timestamp (uses default pattern).
// Deprecated: use PathPattern.FormatDir instead.
func segmentDir(basePath, camera string, t time.Time) string {
	return fmt.Sprintf("%s/%s/%s", basePath, camera, t.Format("2006-01-02"))
}

// segmentPath returns the full path for a segment file (uses default pattern).
// Deprecated: use PathPattern.FormatFull instead.
func segmentPath(basePath, camera string, t time.Time) string {
	return segmentDir(basePath, camera, t) + "/" + t.Format("15-04-05") + ".mp4"
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
