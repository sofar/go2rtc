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

	// Create an "old" file
	oldDir := filepath.Join(dir, "cam1", "2020-01-01")
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	oldFile := filepath.Join(oldDir, "12-00-00.mp4")
	f, _ := os.Create(oldFile)
	f.Close()
	// Set mod time to 2 days ago
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldFile, old, old)

	// Create a "new" file
	newDir := filepath.Join(dir, "cam1", "2099-01-01")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	newFile := filepath.Join(newDir, "12-00-00.mp4")
	f, _ = os.Create(newFile)
	f.Close()

	// Retain only 24 hours
	enforceRetention(dir, RetentionPolicy{MaxAge: 24 * time.Hour})

	// Old file should be removed
	_, err := os.Stat(oldFile)
	assert.True(t, os.IsNotExist(err), "old file should be removed")

	// Old empty directory should be removed
	_, err = os.Stat(oldDir)
	assert.True(t, os.IsNotExist(err), "empty dir should be removed")

	// New file should remain
	_, err = os.Stat(newFile)
	assert.NoError(t, err, "new file should remain")
}

func TestRetention_BySize(t *testing.T) {
	dir := t.TempDir()

	camDir := filepath.Join(dir, "cam1", "2026-01-01")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create 3 files of 100 bytes each, with staggered mod times
	for i, name := range []string{"10-00-00.mp4", "11-00-00.mp4", "12-00-00.mp4"} {
		path := filepath.Join(camDir, name)
		require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o644))
		ts := time.Now().Add(time.Duration(i-3) * time.Hour)
		os.Chtimes(path, ts, ts)
	}

	// Cap at 200 bytes — oldest file should be removed
	enforceRetention(dir, RetentionPolicy{MaxSize: 200})

	_, err := os.Stat(filepath.Join(camDir, "10-00-00.mp4"))
	assert.True(t, os.IsNotExist(err), "oldest file should be removed")

	_, err = os.Stat(filepath.Join(camDir, "11-00-00.mp4"))
	assert.NoError(t, err, "second file should remain")

	_, err = os.Stat(filepath.Join(camDir, "12-00-00.mp4"))
	assert.NoError(t, err, "newest file should remain")
}

func TestRetention_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	// No files — should not panic
	enforceRetention(dir, RetentionPolicy{MaxAge: 24 * time.Hour})
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
