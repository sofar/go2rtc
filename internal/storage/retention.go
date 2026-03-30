package storage

import (
	"os"
	"path/filepath"
	"time"
)

// runRetention periodically enforces the retention policy using the segment
// database. Only files tracked in the DB (i.e. actual recordings) are removed.
func runRetention(basePath string, policy RetentionPolicy, interval time.Duration, db *SegmentDB) {
	if policy.IsZero() || db == nil {
		return
	}

	ticker := time.NewTicker(interval)
	go func() {
		// Run immediately on start, then on ticker
		enforceRetention(basePath, policy, db)
		for range ticker.C {
			enforceRetention(basePath, policy, db)
		}
	}()
}

// enforceRetention applies the retention policy: time-based, size-based, or both.
// Only segments tracked in the database are candidates for deletion.
func enforceRetention(basePath string, policy RetentionPolicy, db *SegmentDB) {
	if policy.MaxAge > 0 {
		cleanByAge(db, policy.MaxAge)
	}
	if policy.MaxSize > 0 {
		cleanBySize(db, policy.MaxSize)
	}
	cleanEmptyDirs(basePath)
}

// cleanByAge removes tracked segments older than maxAge.
func cleanByAge(db *SegmentDB, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)

	paths, err := db.OlderThan(cutoff)
	if err != nil {
		log.Error().Err(err).Msg("[storage] retention: query old segments")
		return
	}

	removed := 0
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", path).Msg("[storage] retention: remove")
			continue
		}
		if err := db.Delete(path); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("[storage] retention: db delete")
			continue
		}
		removed++
	}

	if removed > 0 {
		log.Info().Int("removed", removed).Msg("[storage] retention: age cleanup")
	}
}

// cleanBySize removes oldest tracked segments until total size is under maxSize.
func cleanBySize(db *SegmentDB, maxSize int64) {
	entries, totalSize, err := db.OldestBySize(maxSize)
	if err != nil {
		log.Error().Err(err).Msg("[storage] retention: query segments by size")
		return
	}
	if len(entries) == 0 {
		return // already under budget
	}

	removed := 0
	for _, e := range entries {
		if totalSize <= maxSize {
			break
		}
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", e.path).Msg("[storage] retention: remove")
			continue
		}
		if err := db.Delete(e.path); err != nil {
			log.Warn().Err(err).Str("path", e.path).Msg("[storage] retention: db delete")
			continue
		}
		totalSize -= e.size
		removed++
	}

	if removed > 0 {
		log.Info().Int("removed", removed).Int64("freed_to", totalSize).
			Msg("[storage] retention: size cleanup")
	}
}

// cleanEmptyDirs removes empty directories under basePath.
func cleanEmptyDirs(basePath string) {
	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || path == basePath {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err == nil && len(entries) == 0 {
			os.Remove(path)
		}
		return nil
	})
}
