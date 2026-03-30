package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSegmentDB_InsertAndQuery(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenSegmentDB(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	now := time.Now()
	old := now.Add(-48 * time.Hour)

	require.NoError(t, db.Insert("/data/cam1/old.mp4", "cam1", old, 1000))
	require.NoError(t, db.Insert("/data/cam1/new.mp4", "cam1", now, 2000))

	// OlderThan should return only the old segment
	paths, err := db.OlderThan(now.Add(-24 * time.Hour))
	require.NoError(t, err)
	assert.Equal(t, []string{"/data/cam1/old.mp4"}, paths)

	// Delete and verify
	require.NoError(t, db.Delete("/data/cam1/old.mp4"))
	paths, err = db.OlderThan(now.Add(-24 * time.Hour))
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestSegmentDB_OldestBySize(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenSegmentDB(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	now := time.Now()

	// Insert 3 segments totaling 300 bytes
	require.NoError(t, db.Insert("/a.mp4", "cam1", now.Add(-3*time.Hour), 100))
	require.NoError(t, db.Insert("/b.mp4", "cam1", now.Add(-2*time.Hour), 100))
	require.NoError(t, db.Insert("/c.mp4", "cam1", now.Add(-1*time.Hour), 100))

	// Under budget — nothing returned
	entries, total, err := db.OldestBySize(400)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, int64(300), total)

	// Over budget — all returned for caller to iterate
	entries, total, err = db.OldestBySize(200)
	require.NoError(t, err)
	assert.Equal(t, int64(300), total)
	assert.Len(t, entries, 3)
	assert.Equal(t, "/a.mp4", entries[0].path) // oldest first
}

func TestSegmentDB_Upsert(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenSegmentDB(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	now := time.Now()

	// Insert then update size
	require.NoError(t, db.Insert("/a.mp4", "cam1", now, 100))
	require.NoError(t, db.Insert("/a.mp4", "cam1", now, 200))

	// Should have only one entry with updated size
	entries, total, err := db.OldestBySize(0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, int64(200), total)
}
