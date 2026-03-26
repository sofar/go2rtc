// Package upload provides asynchronous file uploading to remote storage
// backends (S3, FTP, SMB). When configured, it automatically uploads
// completed recording segments and session snapshots.
//
// Configuration:
//
//	upload:
//	  retention: 90d
//	  s3:
//	    bucket: my-cameras
//	    region: us-east-1
//	  ftp:
//	    host: ftp.example.com:21
//	    username: user
//	    password: pass
//	    path: /recordings/
//	  smb:
//	    host: //nas.local/share
//	    username: user
//	    password: pass
//	    path: /recordings/
package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/storage"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Config for the upload module.
type Config struct {
	Retention  string `yaml:"retention"`  // remote retention (e.g. "90d")
	MaxRetries int    `yaml:"max_retries"`
	RetryDelay string `yaml:"retry_delay"`

	S3  *S3Config  `yaml:"s3"`
	FTP *FTPConfig `yaml:"ftp"`
	SMB *SMBConfig `yaml:"smb"`
}

// RemoteSegment represents a file on the remote backend.
type RemoteSegment struct {
	Path    string    // relative path (e.g. "recordings/amcrest/2026-01-15/14-30-00.mp4")
	Size    int64     // bytes
	ModTime time.Time // last modified
}

// Backend abstracts a remote storage destination.
type Backend interface {
	// Upload sends a local file to the remote path.
	Upload(ctx context.Context, localPath, remotePath string) error
	// Download retrieves a remote file and writes it to localPath.
	Download(ctx context.Context, remotePath, localPath string) error
	// List returns remote files under a prefix.
	List(ctx context.Context, prefix string) ([]RemoteSegment, error)
	Close() error
	Name() string
}

// uploadJob is a pending upload.
type uploadJob struct {
	LocalPath  string
	RemotePath string
	Retries    int
}

var (
	backends   map[string]Backend
	jobs       chan uploadJob
	maxRetries int
	retryDelay time.Duration
	wg         sync.WaitGroup

	// metrics
	uploaded atomic.Int64
	failed   atomic.Int64
	pending  atomic.Int64
)

func Init() {
	log = app.GetLogger("upload")

	var cfg struct {
		Mod Config `yaml:"upload"`
	}
	app.LoadConfig(&cfg)

	backends = make(map[string]Backend)

	if cfg.Mod.S3 != nil && cfg.Mod.S3.Bucket != "" {
		b, err := NewS3Backend(cfg.Mod.S3)
		if err != nil {
			log.Error().Err(err).Msg("[upload] s3 init")
		} else {
			backends["s3"] = b
			log.Info().Str("bucket", cfg.Mod.S3.Bucket).Msg("[upload] s3 enabled")
		}
	}

	if cfg.Mod.FTP != nil && cfg.Mod.FTP.Host != "" {
		backends["ftp"] = NewFTPBackend(cfg.Mod.FTP)
		log.Info().Str("host", cfg.Mod.FTP.Host).Msg("[upload] ftp enabled")
	}

	if cfg.Mod.SMB != nil && cfg.Mod.SMB.Host != "" {
		backends["smb"] = NewSMBBackend(cfg.Mod.SMB)
		log.Info().Str("host", cfg.Mod.SMB.Host).Msg("[upload] smb enabled")
	}

	if len(backends) == 0 {
		return
	}

	maxRetries = cfg.Mod.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	retryDelay = 30 * time.Second
	if cfg.Mod.RetryDelay != "" {
		if d, err := time.ParseDuration(cfg.Mod.RetryDelay); err == nil {
			retryDelay = d
		}
	}

	// Buffered job queue
	jobs = make(chan uploadJob, 1024)

	// Start one worker per backend
	for name, b := range backends {
		wg.Add(1)
		go worker(name, b)
	}

	// Hook recording segment uploads
	storage.OnSegmentClosed = enqueueSegment

	// Subscribe to session_end events for snapshot uploads
	if events.Default != nil {
		ch := events.Default.Subscribe(events.Filter{}, 64)
		go func() {
			for e := range ch {
				if e.Type != events.TypeSessionEnd {
					continue
				}
				sess, ok := e.Data.(*events.Session)
				if !ok || sess.ID == "" || events.SessionBasePath == "" {
					continue
				}
				enqueueSession(
					fmt.Sprintf("%s/sessions/%s", events.SessionBasePath, sess.ID),
					sess.ID,
				)
			}
		}()
	}

	// Wire remote retrieval callbacks for storage module (avoids circular import)
	b := FirstBackend()
	if b != nil {
		storage.RemoteDownload = func(ctx context.Context, remotePath, localPath string) error {
			return b.Download(ctx, remotePath, localPath)
		}
		storage.RemoteList = func(ctx context.Context, prefix string) ([]storage.RemoteSegmentInfo, error) {
			segs, err := b.List(ctx, prefix)
			if err != nil {
				return nil, err
			}
			result := make([]storage.RemoteSegmentInfo, len(segs))
			for i, s := range segs {
				result[i] = storage.RemoteSegmentInfo{
					Path:    s.Path,
					Size:    s.Size,
					ModTime: s.ModTime,
				}
			}
			return result, nil
		}

		// Start remote retention if configured
		if cfg.Mod.Retention != "" {
			policy, err := storage.ParseRetention(cfg.Mod.Retention)
			if err != nil {
				log.Error().Err(err).Str("retention", cfg.Mod.Retention).Msg("[upload] invalid retention")
			} else if !policy.IsZero() {
				runRemoteRetention(policy)
				log.Info().Str("retention", cfg.Mod.Retention).Msg("[upload] remote retention enabled")
			}
		}
	}

	api.HandleFunc("api/upload/status", apiStatus)

	log.Info().Int("backends", len(backends)).Msg("[upload] started")
}

func enqueueSegment(localPath, relPath string) {
	pending.Add(1)
	select {
	case jobs <- uploadJob{LocalPath: localPath, RemotePath: "recordings/" + relPath}:
	default:
		pending.Add(-1)
		log.Warn().Str("path", relPath).Msg("[upload] queue full, dropping segment")
	}
}

func enqueueSession(sessionDir, sessionID string) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		log.Error().Err(err).Str("session", sessionID).Msg("[upload] list session files")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		localPath := sessionDir + "/" + entry.Name()
		remotePath := "sessions/" + sessionID + "/" + entry.Name()
		pending.Add(1)
		select {
		case jobs <- uploadJob{LocalPath: localPath, RemotePath: remotePath}:
		default:
			pending.Add(-1)
			log.Warn().Str("path", remotePath).Msg("[upload] queue full, dropping snapshot")
			return
		}
	}
}

func worker(name string, b Backend) {
	defer wg.Done()
	for job := range jobs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := b.Upload(ctx, job.LocalPath, job.RemotePath)
		cancel()

		if err != nil {
			log.Error().Err(err).
				Str("backend", name).
				Str("path", job.RemotePath).
				Int("retry", job.Retries).
				Msg("[upload] failed")

			if job.Retries < maxRetries {
				job.Retries++
				go func(j uploadJob) {
					time.Sleep(retryDelay * time.Duration(j.Retries))
					select {
					case jobs <- j:
					default:
						pending.Add(-1)
						failed.Add(1)
						log.Warn().Str("path", j.RemotePath).Msg("[upload] retry queue full")
					}
				}(job)
			} else {
				pending.Add(-1)
				failed.Add(1)
			}
		} else {
			pending.Add(-1)
			uploaded.Add(1)
			log.Debug().Str("backend", name).Str("path", job.RemotePath).Msg("[upload] ok")
		}
	}
}

// GetBackend returns a configured backend by name, or nil.
func GetBackend(name string) Backend {
	if backends == nil {
		return nil
	}
	return backends[name]
}

// FirstBackend returns any configured backend, or nil.
// Used by storage for transparent remote retrieval.
func FirstBackend() Backend {
	for _, b := range backends {
		return b
	}
	return nil
}

// ListRemoteSegments lists recording segments on the first available backend.
func ListRemoteSegments(ctx context.Context, camera string) ([]RemoteSegment, error) {
	b := FirstBackend()
	if b == nil {
		return nil, nil
	}
	prefix := "recordings/" + camera + "/"
	return b.List(ctx, prefix)
}

// DownloadSegment fetches a segment from the first available backend to a local path.
func DownloadSegment(ctx context.Context, remotePath, localPath string) error {
	b := FirstBackend()
	if b == nil {
		return fmt.Errorf("no upload backend configured")
	}
	return b.Download(ctx, remotePath, localPath)
}

// runRemoteRetention periodically enforces the retention policy on remote storage.
func runRemoteRetention(policy storage.RetentionPolicy) {
	go func() {
		enforceRemoteRetention(policy)
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			enforceRemoteRetention(policy)
		}
	}()
}

func enforceRemoteRetention(policy storage.RetentionPolicy) {
	b := FirstBackend()
	if b == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	segments, err := b.List(ctx, "recordings/")
	if err != nil {
		log.Error().Err(err).Msg("[upload] remote retention: list failed")
		return
	}

	removed := 0

	// Age-based cleanup
	if policy.MaxAge > 0 {
		cutoff := time.Now().Add(-policy.MaxAge)
		for _, seg := range segments {
			if seg.ModTime.Before(cutoff) {
				if deleteRemoteFile(b, seg.Path) {
					removed++
				}
			}
		}
	}

	// Size-based cleanup: delete oldest until under limit
	if policy.MaxSize > 0 {
		var totalSize int64
		for _, seg := range segments {
			totalSize += seg.Size
		}
		if totalSize > policy.MaxSize {
			// Sort oldest first
			sort.Slice(segments, func(i, j int) bool {
				return segments[i].ModTime.Before(segments[j].ModTime)
			})
			for _, seg := range segments {
				if totalSize <= policy.MaxSize {
					break
				}
				if deleteRemoteFile(b, seg.Path) {
					totalSize -= seg.Size
					removed++
				}
			}
		}
	}

	if removed > 0 {
		log.Info().Int("removed", removed).Msg("[upload] remote retention cleanup")
	}
}

func deleteRemoteFile(b Backend, remotePath string) bool {
	type deleter interface {
		Delete(ctx context.Context, remotePath string) error
	}
	d, ok := b.(deleter)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Delete(ctx, remotePath); err != nil {
		log.Error().Err(err).Str("path", remotePath).Msg("[upload] remote retention: delete failed")
		return false
	}
	return true
}

// resolvePassword returns password from the file if password_file is set,
// otherwise returns the inline password.
func resolvePassword(password, passwordFile string) string {
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile)
		if err != nil {
			log.Error().Err(err).Str("file", passwordFile).Msg("[upload] read password_file")
			return password
		}
		// Trim trailing newline — common in secret files
		return strings.TrimRight(string(data), "\r\n")
	}
	return password
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}

	status := map[string]any{
		"backends": names,
		"uploaded": uploaded.Load(),
		"failed":   failed.Load(),
		"pending":  pending.Load(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
