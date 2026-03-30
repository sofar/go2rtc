package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SegmentDB tracks recorded segments in a SQLite database so that retention
// only deletes files that were created by the recorder — not arbitrary files
// that happen to live under the same base path.
type SegmentDB struct {
	db *sql.DB
	mu sync.Mutex
}

// OpenSegmentDB opens (or creates) the segment tracking database at dbPath.
func OpenSegmentDB(dbPath string) (*SegmentDB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)")
	if err != nil {
		return nil, err
	}

	// Create the table if it doesn't exist.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS segments (
		path       TEXT PRIMARY KEY,
		camera     TEXT NOT NULL,
		start_time INTEGER NOT NULL,
		size       INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// Index for age-based queries.
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_segments_start ON segments (start_time)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &SegmentDB{db: db}, nil
}

// Insert registers a recorded segment in the database.
func (s *SegmentDB) Insert(path, camera string, startTime time.Time, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO segments (path, camera, start_time, size, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		path, camera, startTime.Unix(), size, time.Now().Unix(),
	)
	return err
}

// OlderThan returns paths of segments with start_time before the cutoff.
func (s *SegmentDB) OlderThan(cutoff time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT path FROM segments WHERE start_time < ? ORDER BY start_time`,
		cutoff.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// OldestBySize returns the oldest segment paths that should be removed to
// bring total tracked size at or below maxSize. Segments are ordered oldest
// first; the caller should delete them until the budget is satisfied.
func (s *SegmentDB) OldestBySize(maxSize int64) ([]segmentSizeEntry, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get total size.
	var totalSize int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM segments`).Scan(&totalSize); err != nil {
		return nil, 0, err
	}
	if totalSize <= maxSize {
		return nil, totalSize, nil
	}

	rows, err := s.db.Query(`SELECT path, size FROM segments ORDER BY start_time ASC`)
	if err != nil {
		return nil, totalSize, err
	}
	defer rows.Close()

	var entries []segmentSizeEntry
	for rows.Next() {
		var e segmentSizeEntry
		if err := rows.Scan(&e.path, &e.size); err != nil {
			return nil, totalSize, err
		}
		entries = append(entries, e)
	}
	return entries, totalSize, rows.Err()
}

type segmentSizeEntry struct {
	path string
	size int64
}

// Delete removes a segment record from the database.
func (s *SegmentDB) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM segments WHERE path = ?`, path)
	return err
}

// Close closes the database.
func (s *SegmentDB) Close() error {
	return s.db.Close()
}
