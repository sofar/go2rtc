package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// persistedState is the JSON structure saved to disk.
type persistedState struct {
	Active []*Session `json:"active"`
	Ended  []*Session `json:"ended"`
	SeqID  int        `json:"seq_id"`
}

const sessionsFile = "sessions_state.json"

// SaveState writes the tracker's current state to disk.
func (t *Tracker) SaveState(basePath string) error {
	if basePath == "" {
		return nil
	}

	t.mu.Lock()
	state := persistedState{
		SeqID: t.seqID,
	}
	for _, s := range t.active {
		cp := *s
		cp.Duration = time.Since(s.Start).Seconds()
		state.Active = append(state.Active, &cp)
	}
	state.Ended = make([]*Session, len(t.ended))
	copy(state.Ended, t.ended)
	t.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(basePath, sessionsFile)
	return os.WriteFile(path, data, 0o644)
}

// LoadState restores the tracker's state from disk. Active sessions
// that were running when the process stopped are moved to ended
// (since we don't know if the object is still there).
func (t *Tracker) LoadState(basePath string) error {
	if basePath == "" {
		return nil
	}

	path := filepath.Join(basePath, sessionsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no saved state
		}
		return err
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Restore sequence counter
	if state.SeqID > t.seqID {
		t.seqID = state.SeqID
	}

	// Previously active sessions are marked as ended (we can't
	// know if the object is still there after a restart)
	for _, s := range state.Active {
		s.Active = false
		s.End = s.LastSeen
		s.Duration = s.End.Sub(s.Start).Seconds()
		t.ended = append(t.ended, s)
	}

	// Restore ended sessions
	t.ended = append(t.ended, state.Ended...)

	// Cap the ended list
	if len(t.ended) > maxEndedSessions {
		t.ended = t.ended[len(t.ended)-maxEndedSessions:]
	}

	log.Info().
		Int("restored_ended", len(state.Active)+len(state.Ended)).
		Int("seq_id", t.seqID).
		Msg("[events] sessions restored from disk")

	return nil
}

// startPeriodicSave saves state every 30 seconds and on stop.
func (t *Tracker) startPeriodicSave(basePath string) {
	if basePath == "" {
		return
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-t.stop:
				// Final save on shutdown
				t.SaveState(basePath)
				return
			case <-ticker.C:
				if err := t.SaveState(basePath); err != nil {
					log.Error().Err(err).Msg("[events] save sessions")
				}
			}
		}
	}()
}
