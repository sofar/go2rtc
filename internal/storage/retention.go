package storage

import (
	"os"
	"path/filepath"
	"time"
)

// runRetention periodically scans basePath and removes segment files
// older than maxAge.
func runRetention(basePath string, maxAge time.Duration, interval time.Duration) {
	if maxAge <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	go func() {
		// Run immediately on start, then on ticker
		cleanOldSegments(basePath, maxAge)
		for range ticker.C {
			cleanOldSegments(basePath, maxAge)
		}
	}()
}

// cleanOldSegments removes segment files and empty directories older
// than maxAge under basePath.
func cleanOldSegments(basePath string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	removed := 0

	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if info.IsDir() {
			return nil
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})

	// Clean empty date directories
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

	if removed > 0 {
		log.Info().Int("removed", removed).Str("path", basePath).Msg("[storage] retention cleanup")
	}
}
