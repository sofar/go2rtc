package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPattern() *PathPattern {
	return NewPathPattern("")
}

func TestNewRecorder_DefaultSegDur(t *testing.T) {
	rec := NewRecorder("cam1", "/tmp/test", testPattern(), 0, "")
	assert.Equal(t, 5*time.Minute, rec.segDur)
	assert.Equal(t, "cam1", rec.camera)
	assert.Equal(t, "/tmp/test", rec.basePath)
}

func TestNewRecorder_CustomSegDur(t *testing.T) {
	rec := NewRecorder("cam1", "/tmp/test", testPattern(), 10*time.Minute, "")
	assert.Equal(t, 10*time.Minute, rec.segDur)
}

func TestRecorder_GetMedias(t *testing.T) {
	rec := NewRecorder("cam1", "/tmp/test", testPattern(), 0, "")
	medias := rec.GetMedias()
	require.Len(t, medias, 2)

	// Video
	assert.Equal(t, core.KindVideo, medias[0].Kind)
	assert.Equal(t, core.DirectionSendonly, medias[0].Direction)

	// Audio
	assert.Equal(t, core.KindAudio, medias[1].Kind)
}

func TestRecorder_Stop_NoFile(t *testing.T) {
	rec := NewRecorder("cam1", "/tmp/test", testPattern(), 0, "")
	assert.NoError(t, rec.Stop())
}

func TestListSegments_Empty(t *testing.T) {
	globalPattern = testPattern()
	dir := t.TempDir()
	segments := listSegments(dir, "cam1", time.Time{}, time.Now())
	assert.Empty(t, segments)
}

func TestListSegments_FindsFiles(t *testing.T) {
	globalPattern = testPattern()
	dir := t.TempDir()
	camDir := filepath.Join(dir, "cam1", "2024-03-15")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create some segment files
	for _, name := range []string{"10-00-00.mp4", "10-05-00.mp4", "10-10-00.mp4"} {
		f, err := os.Create(filepath.Join(camDir, name))
		require.NoError(t, err)
		f.Write([]byte("fake mp4 data"))
		f.Close()
	}

	// Also create a non-mp4 file that should be ignored
	os.Create(filepath.Join(camDir, "notes.txt"))

	segments := listSegments(dir, "cam1", time.Time{}, time.Now())
	require.Len(t, segments, 3)

	// Should be sorted by time (newest first)
	assert.True(t, segments[0].Start.After(segments[1].Start))
	assert.True(t, segments[1].Start.After(segments[2].Start))

	// Check fields
	assert.Equal(t, "cam1", segments[0].Camera)
	assert.Greater(t, segments[0].Size, int64(0))
}

func TestListSegments_TimeFilter(t *testing.T) {
	globalPattern = testPattern()
	dir := t.TempDir()
	camDir := filepath.Join(dir, "cam1", "2024-03-15")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	for _, name := range []string{"08-00-00.mp4", "12-00-00.mp4", "16-00-00.mp4"} {
		f, _ := os.Create(filepath.Join(camDir, name))
		f.Write([]byte("data"))
		f.Close()
	}

	from := time.Date(2024, 3, 15, 10, 0, 0, 0, time.Local)
	to := time.Date(2024, 3, 15, 14, 0, 0, 0, time.Local)

	segments := listSegments(dir, "cam1", from, to)
	require.Len(t, segments, 1)
	assert.Equal(t, "cam1/2024-03-15/12-00-00.mp4", segments[0].Path)
}
