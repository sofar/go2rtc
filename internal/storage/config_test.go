package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input  string
		expect time.Duration
		err    bool
	}{
		{"5m", 5 * time.Minute, false},
		{"1h", time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"14d", 14 * 24 * time.Hour, false},
		{"", 0, false},
		{"abc", 0, true},
		{"xd", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			d, err := parseDuration(tt.input)
			if tt.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expect, d)
			}
		})
	}
}

func TestPathPattern_Default(t *testing.T) {
	p := NewPathPattern("")
	ts := time.Date(2024, 3, 15, 14, 30, 45, 0, time.Local)

	// Format
	assert.Equal(t, "front_cam/2024-03-15/14-30-45", p.Format("front_cam", ts))
	assert.Equal(t, "/data/front_cam/2024-03-15/14-30-45.mp4", p.FormatFull("/data", "front_cam", ts, ".mp4"))

	// Parse
	cam, parsed, ok := p.Parse("front_cam/2024-03-15/14-30-45.mp4")
	assert.True(t, ok)
	assert.Equal(t, "front_cam", cam)
	assert.Equal(t, "2024-03-15T14:30:45", parsed.Format("2006-01-02T15:04:05"))
}

func TestPathPattern_Flat(t *testing.T) {
	p := NewPathPattern("{camera}_{year}{month}{day}_{hour}{minute}{second}")
	ts := time.Date(2026, 1, 5, 8, 15, 30, 0, time.Local)

	assert.Equal(t, "lobby_20260105_081530", p.Format("lobby", ts))

	cam, parsed, ok := p.Parse("lobby_20260105_081530.mp4")
	assert.True(t, ok)
	assert.Equal(t, "lobby", cam)
	assert.Equal(t, "2026-01-05T08:15:30", parsed.Format("2006-01-02T15:04:05"))
}

func TestPathPattern_DateFirst(t *testing.T) {
	p := NewPathPattern("{year}/{month}/{day}/{camera}/{hour}-{minute}-{second}")
	ts := time.Date(2026, 3, 25, 19, 45, 0, 0, time.Local)

	assert.Equal(t, "2026/03/25/porch/19-45-00", p.Format("porch", ts))

	cam, parsed, ok := p.Parse("2026/03/25/porch/19-45-00.ts")
	assert.True(t, ok)
	assert.Equal(t, "porch", cam)
	assert.Equal(t, "2026-03-25T19:45:00", parsed.Format("2006-01-02T15:04:05"))
}

func TestPathPattern_NoMatch(t *testing.T) {
	p := NewPathPattern("")
	_, _, ok := p.Parse("bad-path.mp4")
	assert.False(t, ok)

	_, _, ok = p.Parse("a/b/c/d.mp4")
	assert.False(t, ok)
}

func TestPathPattern_FormatDir(t *testing.T) {
	p := NewPathPattern("")
	ts := time.Date(2024, 12, 1, 0, 0, 0, 0, time.Local)
	dir := p.FormatDir("/data", "cam1", ts)
	assert.Equal(t, "/data/cam1/2024-12-01", dir)
}

// Backward compat: deprecated helpers still work
func TestSegmentPath(t *testing.T) {
	ts := time.Date(2024, 3, 15, 14, 30, 45, 0, time.Local)
	path := segmentPath("/recordings", "front_cam", ts)
	assert.Equal(t, "/recordings/front_cam/2024-03-15/14-30-45.mp4", path)
}

func TestSegmentDir(t *testing.T) {
	ts := time.Date(2024, 12, 1, 0, 0, 0, 0, time.Local)
	dir := segmentDir("/data", "cam1", ts)
	assert.Equal(t, "/data/cam1/2024-12-01", dir)
}
