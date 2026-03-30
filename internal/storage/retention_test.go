package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetention_ByAge(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenSegmentDB(filepath.Join(dir, "segments.db"))
	require.NoError(t, err)
	defer db.Close()

	// Create an "old" file and track it
	oldDir := filepath.Join(dir, "cam1", "2020-01-01")
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	oldFile := filepath.Join(oldDir, "12-00-00.mp4")
	f, _ := os.Create(oldFile)
	f.Close()
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, db.Insert(oldFile, "cam1", oldTime, 0))

	// Create a "new" file and track it
	newDir := filepath.Join(dir, "cam1", "2099-01-01")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	newFile := filepath.Join(newDir, "12-00-00.mp4")
	f, _ = os.Create(newFile)
	f.Close()
	require.NoError(t, db.Insert(newFile, "cam1", time.Now(), 0))

	// Create an UNTRACKED file (e.g. a model file) — must survive retention
	untrackedFile := filepath.Join(dir, "yolo-model.onnx")
	require.NoError(t, os.WriteFile(untrackedFile, []byte("model data"), 0o644))
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(untrackedFile, old, old)

	// Retain only 24 hours
	enforceRetention(dir, RetentionPolicy{MaxAge: 24 * time.Hour}, db)

	// Old tracked file should be removed
	_, err = os.Stat(oldFile)
	assert.True(t, os.IsNotExist(err), "old tracked file should be removed")

	// Old empty directory should be removed
	_, err = os.Stat(oldDir)
	assert.True(t, os.IsNotExist(err), "empty dir should be removed")

	// New tracked file should remain
	_, err = os.Stat(newFile)
	assert.NoError(t, err, "new file should remain")

	// Untracked file must NOT be removed
	_, err = os.Stat(untrackedFile)
	assert.NoError(t, err, "untracked file must survive retention")
}

func TestRetention_BySize(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenSegmentDB(filepath.Join(dir, "segments.db"))
	require.NoError(t, err)
	defer db.Close()

	camDir := filepath.Join(dir, "cam1", "2026-01-01")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create 3 tracked files of 100 bytes each, with staggered start times
	for i, name := range []string{"10-00-00.mp4", "11-00-00.mp4", "12-00-00.mp4"} {
		path := filepath.Join(camDir, name)
		require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o644))
		startTime := time.Now().Add(time.Duration(i-3) * time.Hour)
		require.NoError(t, db.Insert(path, "cam1", startTime, 100))
	}

	// Create an untracked file — must survive
	untrackedFile := filepath.Join(dir, "my-data.bin")
	require.NoError(t, os.WriteFile(untrackedFile, make([]byte, 500), 0o644))

	// Cap at 200 bytes — oldest tracked file should be removed
	enforceRetention(dir, RetentionPolicy{MaxSize: 200}, db)

	_, err = os.Stat(filepath.Join(camDir, "10-00-00.mp4"))
	assert.True(t, os.IsNotExist(err), "oldest tracked file should be removed")

	_, err = os.Stat(filepath.Join(camDir, "11-00-00.mp4"))
	assert.NoError(t, err, "second file should remain")

	_, err = os.Stat(filepath.Join(camDir, "12-00-00.mp4"))
	assert.NoError(t, err, "newest file should remain")

	// Untracked file must survive
	_, err = os.Stat(untrackedFile)
	assert.NoError(t, err, "untracked file must survive retention")
}

func TestRetention_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	db, err := OpenSegmentDB(filepath.Join(dir, "segments.db"))
	require.NoError(t, err)
	defer db.Close()

	// No files — should not panic
	enforceRetention(dir, RetentionPolicy{MaxAge: 24 * time.Hour}, db)
}

func TestParseRetention(t *testing.T) {
	tests := []struct {
		input   string
		wantAge time.Duration
		wantSz  int64
	}{
		{"7d", 7 * 24 * time.Hour, 0},
		{"24h", 24 * time.Hour, 0},
		{"500M", 0, 500 * (1 << 20)},
		{"1G", 0, 1 << 30},
		{"2T", 0, 2 * (1 << 40)},
		{"500000000", 0, 500000000},
		{"", 0, 0},
	}

	for _, tt := range tests {
		p, err := ParseRetention(tt.input)
		assert.NoError(t, err, "input: %s", tt.input)
		assert.Equal(t, tt.wantAge, p.MaxAge, "age for %s", tt.input)
		assert.Equal(t, tt.wantSz, p.MaxSize, "size for %s", tt.input)
	}
}
