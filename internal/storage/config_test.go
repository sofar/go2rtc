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

func TestParseSegmentTime(t *testing.T) {
	tests := []struct {
		input  string
		expect string
		ok     bool
	}{
		{"2024-03-15/14-30-45.mp4", "2024-03-15T14:30:45", true},
		{"2024-01-01/00-00-00.mp4", "2024-01-01T00:00:00", true},
		{"bad-path.mp4", "", false},
		{"a/b/c.mp4", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseSegmentTime(tt.input)
			if !tt.ok {
				assert.True(t, result.IsZero())
			} else {
				assert.Equal(t, tt.expect, result.Format("2006-01-02T15:04:05"))
			}
		})
	}
}
