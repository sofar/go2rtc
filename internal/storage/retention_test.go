package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanOldSegments(t *testing.T) {
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
	cleanOldSegments(dir, 24*time.Hour)

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

func TestCleanOldSegments_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	// No files — should not panic
	cleanOldSegments(dir, 24*time.Hour)
}
