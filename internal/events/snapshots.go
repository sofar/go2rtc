package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SnapshotStore captures and stores annotated JPEG snapshots for
// active detection sessions. Saves the first frame, periodic frames
// (every interval), and the last frame.
type SnapshotStore struct {
	basePath string
	interval time.Duration
	bus      *Bus
	tracker  *Tracker

	// captureFunc is called to get an annotated JPEG for a camera+region.
	// Set via RegisterSnapshotCapture from the snapshot module.
	captureFunc SnapshotCaptureFunc

	mu       sync.Mutex
	sessions map[sessionKey]*sessionSnaps
	stop     chan struct{}
}

// SnapshotCaptureFunc captures an annotated JPEG for a camera in a region.
type SnapshotCaptureFunc func(camera, region string) []byte

var snapshotCapture SnapshotCaptureFunc

// RegisterSnapshotCapture sets the function used to capture annotated
// snapshots. Called by the snapshot module during init.
func RegisterSnapshotCapture(fn SnapshotCaptureFunc) {
	snapshotCapture = fn
}

const maxSnapshotsPerSession = 50

type sessionSnaps struct {
	sessionID string
	dir       string
	count     int
	lastSnap  time.Time
	startTime time.Time
	cameras   []string
	region    string
}

// SessionSnapshot holds info about a stored snapshot.
type SessionSnapshot struct {
	Index    int       `json:"index"`
	Time     time.Time `json:"time"`
	Camera   string    `json:"camera"`
	Filename string    `json:"filename"`
}

// StoredSession is the persisted session with snapshot references.
type StoredSession struct {
	Session   *Session           `json:"session"`
	Snapshots []SessionSnapshot  `json:"snapshots"`
}

// NewSnapshotStore creates a store that saves session snapshots to disk.
func NewSnapshotStore(basePath string, interval time.Duration, bus *Bus, tracker *Tracker) *SnapshotStore {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &SnapshotStore{
		basePath: basePath,
		interval: interval,
		bus:      bus,
		tracker:  tracker,
		sessions: make(map[sessionKey]*sessionSnaps),
		stop:     make(chan struct{}),
	}
}

// Start begins listening for session events and capturing snapshots.
func (s *SnapshotStore) Start() {
	ch := s.bus.Subscribe(Filter{}, 128)

	go func() {
		for {
			select {
			case <-s.stop:
				s.bus.Unsubscribe(ch)
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				s.handleEvent(e)
			}
		}
	}()

	// Periodic snapshot capture for active sessions
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.captureActive()
			}
		}
	}()
}

func (s *SnapshotStore) Stop() {
	close(s.stop)
}

func (s *SnapshotStore) handleEvent(e *Event) {
	switch e.Type {
	case TypeSessionStart:
		sess, ok := e.Data.(*Session)
		if !ok {
			return
		}
		s.onSessionStart(sess)

	case TypeSessionEnd:
		sess, ok := e.Data.(*Session)
		if !ok {
			return
		}
		s.onSessionEnd(sess)
	}
}

func (s *SnapshotStore) onSessionStart(sess *Session) {
	key := sessionKey{region: sess.Region, class: sess.Class}

	dir := filepath.Join(s.basePath, "sessions", sess.ID)
	os.MkdirAll(dir, 0o755)

	s.mu.Lock()
	s.sessions[key] = &sessionSnaps{
		sessionID: sess.ID,
		dir:       dir,
		startTime: time.Now(),
		cameras:   sess.Cameras,
		region:    sess.Region,
	}
	s.mu.Unlock()

	// Capture first snapshot immediately
	s.captureSnapshot(key)
}

func (s *SnapshotStore) onSessionEnd(sess *Session) {
	key := sessionKey{region: sess.Region, class: sess.Class}

	// Capture final snapshot
	s.captureSnapshot(key)

	s.mu.Lock()
	snaps, ok := s.sessions[key]
	if ok {
		delete(s.sessions, key)
	}
	s.mu.Unlock()

	if !ok || snaps == nil {
		return
	}

	// Save session metadata
	s.saveSessionMeta(snaps, sess)
}

func (s *SnapshotStore) captureActive() {
	if snapshotCapture == nil {
		return
	}

	s.mu.Lock()
	var toCapture []sessionKey
	now := time.Now()
	for key, snaps := range s.sessions {
		if snaps.count >= maxSnapshotsPerSession {
			continue
		}
		interval := s.adaptiveInterval(now.Sub(snaps.startTime))
		if now.Sub(snaps.lastSnap) >= interval {
			toCapture = append(toCapture, key)
		}
	}
	s.mu.Unlock()

	for _, key := range toCapture {
		s.captureSnapshot(key)
	}
}

// adaptiveInterval returns the snapshot interval based on session age:
//   - First minute: every 5 seconds
//   - Minutes 2-10: every 30 seconds
//   - After 10 minutes: every 5 minutes
func (s *SnapshotStore) adaptiveInterval(age time.Duration) time.Duration {
	switch {
	case age < time.Minute:
		return s.interval // 5s default
	case age < 10*time.Minute:
		return 30 * time.Second
	default:
		return 5 * time.Minute
	}
}

func (s *SnapshotStore) captureSnapshot(key sessionKey) {
	if snapshotCapture == nil {
		return
	}

	s.mu.Lock()
	snaps, ok := s.sessions[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	cameras := make([]string, len(snaps.cameras))
	copy(cameras, snaps.cameras)

	// Also get cameras from current tracker state
	if s.tracker != nil {
		s.tracker.mu.Lock()
		if active, exists := s.tracker.active[key]; exists {
			for _, cam := range active.Cameras {
				found := false
				for _, c := range cameras {
					if c == cam {
						found = true
						break
					}
				}
				if !found {
					cameras = append(cameras, cam)
				}
			}
		}
		s.tracker.mu.Unlock()
	}

	idx := snaps.count
	dir := snaps.dir
	region := snaps.region
	s.mu.Unlock()

	now := time.Now()

	for _, cam := range cameras {
		data := snapshotCapture(cam, region)
		if len(data) == 0 {
			continue
		}

		filename := fmt.Sprintf("snap-%03d-%s.jpg", idx, cam)
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Error().Err(err).Str("path", path).Msg("[events] save snapshot")
		}
	}

	s.mu.Lock()
	if snaps, ok := s.sessions[key]; ok {
		snaps.count = idx + 1
		snaps.lastSnap = now
	}
	s.mu.Unlock()
}

func (s *SnapshotStore) saveSessionMeta(snaps *sessionSnaps, sess *Session) {
	// List snapshot files
	entries, _ := os.ReadDir(snaps.dir)
	var snapshots []SessionSnapshot
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".jpg" {
			info, _ := entry.Info()
			snapshots = append(snapshots, SessionSnapshot{
				Index:    len(snapshots),
				Time:     info.ModTime(),
				Filename: entry.Name(),
			})
		}
	}

	stored := StoredSession{
		Session:   sess,
		Snapshots: snapshots,
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return
	}
	metaPath := filepath.Join(snaps.dir, "session.json")
	os.WriteFile(metaPath, data, 0o644)
}

// GetStoredSession reads a session's metadata and snapshot list from disk.
func GetStoredSession(basePath, sessionID string) (*StoredSession, error) {
	dir := filepath.Join(basePath, "sessions", sessionID)
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, err
	}

	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

// GetSessionSnapshotPath returns the full path to a session snapshot file.
func GetSessionSnapshotPath(basePath, sessionID, filename string) string {
	return filepath.Join(basePath, "sessions", sessionID, filename)
}

// ListStoredSessions returns recently stored sessions from disk.
func ListStoredSessions(basePath string, limit int) []*StoredSession {
	sessDir := filepath.Join(basePath, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return nil
	}

	var sessions []*StoredSession
	// Read in reverse order (newest first by directory name)
	for i := len(entries) - 1; i >= 0 && len(sessions) < limit; i-- {
		entry := entries[i]
		if !entry.IsDir() {
			continue
		}
		stored, err := GetStoredSession(basePath, entry.Name())
		if err != nil {
			continue
		}
		sessions = append(sessions, stored)
	}
	return sessions
}
