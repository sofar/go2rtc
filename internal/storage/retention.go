package storage

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// runRetention periodically enforces the retention policy on basePath.
func runRetention(basePath string, policy RetentionPolicy, interval time.Duration) {
	if policy.IsZero() {
		return
	}

	ticker := time.NewTicker(interval)
	go func() {
		// Run immediately on start, then on ticker
		enforceRetention(basePath, policy)
		for range ticker.C {
			enforceRetention(basePath, policy)
		}
	}()
}

// enforceRetention applies the retention policy: time-based, size-based, or both.
func enforceRetention(basePath string, policy RetentionPolicy) {
	if policy.MaxAge > 0 {
		cleanByAge(basePath, policy.MaxAge)
	}
	if policy.MaxSize > 0 {
		cleanBySize(basePath, policy.MaxSize)
	}
	cleanEmptyDirs(basePath)
}

// cleanByAge removes files older than maxAge.
func cleanByAge(basePath string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	removed := 0

	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})

	if removed > 0 {
		log.Info().Int("removed", removed).Str("path", basePath).Msg("[storage] retention: age cleanup")
	}
}

type fileEntry struct {
	path    string
	size    int64
	modTime time.Time
}

// cleanBySize removes oldest files until total size is under maxSize.
func cleanBySize(basePath string, maxSize int64) {
	var files []fileEntry
	var totalSize int64

	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, fileEntry{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		totalSize += info.Size()
		return nil
	})

	if totalSize <= maxSize {
		return
	}

	// Sort oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	removed := 0
	for _, f := range files {
		if totalSize <= maxSize {
			break
		}
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
			removed++
		}
	}

	if removed > 0 {
		log.Info().Int("removed", removed).Int64("freed_to", totalSize).Str("path", basePath).
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
