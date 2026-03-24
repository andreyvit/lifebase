package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// State is the persisted daemon/global state.
type State struct {
	Seen   []string `json:"seen"`
	Paused bool     `json:"paused"`
	// LastTGOutput is a small tag describing the most recent message the bot sent to Telegram.
	// It is used to reduce chat noise by suppressing repetitive context in some flows.
	LastTGOutput string `json:"last_tg_output,omitempty"`
	// ProactiveLastRun tracks last run timestamps per proactive item name.
	ProactiveLastRun map[string]time.Time `json:"proactive_last_run"`
	// PendingLog holds a pending log entry request awaiting next message text.
	PendingLog *PendingLogInput `json:"pending_log,omitempty"`
	// LastIncomingAt stores the timestamp of the last incoming item (recording or Telegram voice/text, excluding commands).
	LastIncomingAt time.Time `json:"last_incoming_at,omitzero"`
	// ClaudeSession is the active Claude Code session for automation runs; it is rotated daily.
	ClaudeSession SessionState `json:"claude_session,omitzero"`

	// HealthDailyLastProcessedDay tracks the last local day (YYYY-MM-DD) when
	// end-of-day health snapshot processing ran.
	HealthDailyLastProcessedDay string `json:"health_daily_last_processed_day,omitempty"`
}

type SessionState struct {
	SessionID      string    `json:"session_id"`
	FirstMessageAt time.Time `json:"first_at,omitzero"`
	LastMessageAt  time.Time `json:"last_at,omitzero"`
}

func (s *State) ResetSession() {
	s.ClaudeSession = SessionState{}
}

var (
	state     State
	stateMu   sync.RWMutex
	statePath string
)

// initState loads lifebase-state.json from the repo root (rootDir). If it
// doesn't exist, it initializes from existing audio files. It writes
// lifebase-state.json after migration.
func initState() {
	if strings.TrimSpace(statePath) == "" {
		log.Fatal("statePath is not set (did you call loadConfig?)")
	}

	// 1) Read state file if exists, create new state if not
	var s State
	if b, err := os.ReadFile(statePath); err == nil {
		if e := json.Unmarshal(b, &s); e != nil {
			log.Fatalf("failed to parse %s: %v", filepath.Base(statePath), e)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("failed to read %s: %v", filepath.Base(statePath), err)
	}

	// 2) Do init steps when necessary
	if s.Seen == nil {
		if strings.TrimSpace(audioRecorderDir) != "" {
			entries, err := os.ReadDir(audioRecorderDir)
			if err == nil {
				for _, de := range entries {
					if de.IsDir() {
						continue
					}
					name := de.Name()
					if strings.HasSuffix(strings.ToLower(name), ".m4a") {
						s.Seen = append(s.Seen, name)
					}
				}
				if len(s.Seen) > 0 {
					log.Printf("Initialized Seen with existing audio files (%d)", len(s.Seen))
				}
			}
		}
		if s.Seen == nil {
			s.Seen = []string{}
		}
	}
	if s.ProactiveLastRun == nil {
		s.ProactiveLastRun = make(map[string]time.Time)
	}

	// 3) Persist updated state
	stateMu.Lock()
	defer stateMu.Unlock()
	state = s
	if err := persistStateLocked(); err != nil {
		log.Fatalf("failed to write %s: %v", filepath.Base(statePath), err)
	}
}

// PendingLogInput describes an interactive prompt waiting for a log text.
type PendingLogInput struct {
	FileBasename string    `json:"file_basename"`
	Title        string    `json:"title"`
	ChatID       int64     `json:"chat_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ReadState provides read-only access to the current state under shared lock.
// Do not mutate the state inside the callback.
func ReadState(f func(state *State)) {
	stateMu.RLock()
	defer stateMu.RUnlock()
	f(&state)
}

// UpdateState mutates the state under a write lock and persists it to disk.
func UpdateState(f func(state *State)) {
	stateMu.Lock()
	defer stateMu.Unlock()
	f(&state)
	if err := persistStateLocked(); err != nil {
		log.Fatalf("failed to write %s: %v", filepath.Base(statePath), err)
	}
}

// persistStateLocked marshals and writes the state to the JSON file. Caller must hold stateMu (write) lock.
func persistStateLocked() error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0666); err != nil {
		return err
	}
	return os.Rename(tmp, statePath)
}

// stateHasSeen reports if name is in state.Seen.
func stateHasSeen(name string) bool {
	var found bool
	ReadState(func(s *State) {
		for _, x := range s.Seen {
			if x == name {
				found = true
				return
			}
		}
	})
	return found
}

// stateMarkSeen adds name to state.Seen if missing and persists.
func stateMarkSeen(name string) {
	UpdateState(func(s *State) {
		for _, x := range s.Seen {
			if x == name {
				return
			}
		}
		s.Seen = append(s.Seen, name)
	})
}

// updateLastIncoming updates LastIncomingAt if the provided time is later.
func updateLastIncoming(t time.Time) {
	if t.IsZero() {
		return
	}
	t = t.Local()
	UpdateState(func(s *State) {
		if t.After(s.LastIncomingAt) {
			s.LastIncomingAt = t
		}
	})
}
