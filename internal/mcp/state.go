package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ChannelState persists the session→chatID mapping for the MCP channel.
type ChannelState struct {
	Sessions      map[string]int64 `json:"sessions"`
	LastSessionID string           `json:"last_session_id"`
}

// StateStore manages atomic reads and writes of ChannelState for a single file path.
// Each StateStore instance owns its own mutex, so multiple stores (e.g. in parallel
// tests) do not block each other.
type StateStore struct {
	path string
	mu   sync.Mutex
}

// NewStateStore creates a StateStore for the given file path.
func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

// Read returns the current ChannelState from disk.
// If the file does not exist, an empty state is returned.
func (s *StateStore) Read() (*ChannelState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readStateLocked(s.path)
}

// Write atomically writes state to the store's file with mode 0600.
// Uses os.CreateTemp + os.Rename for atomicity.
func (s *StateStore) Write(state *ChannelState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeStateLocked(s.path, state)
}

// AddSession adds/updates a session→chatID mapping and sets last_session_id.
// Any previous session belonging to the same chatID is removed first.
func (s *StateStore) AddSession(sessionID string, chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := readStateLocked(s.path)
	if err != nil {
		return err
	}
	if state.Sessions == nil {
		state.Sessions = make(map[string]int64)
	}
	for sid, mappedChatID := range state.Sessions {
		if sid != sessionID && mappedChatID == chatID {
			delete(state.Sessions, sid)
		}
	}
	state.Sessions[sessionID] = chatID
	state.LastSessionID = sessionID
	return writeStateLocked(s.path, state)
}

// RemoveSession removes a session from state.
func (s *StateStore) RemoveSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := readStateLocked(s.path)
	if err != nil {
		return err
	}
	delete(state.Sessions, sessionID)
	if state.LastSessionID == sessionID {
		state.LastSessionID = ""
		for k := range state.Sessions {
			state.LastSessionID = k
			break
		}
	}
	return writeStateLocked(s.path, state)
}

// ResolveChatID resolves chatID from state. If sessionID is empty and there is
// exactly one session (or last_session_id is set), returns that chatID.
// Otherwise sessionID is required when multiple sessions exist.
func ResolveChatID(state *ChannelState, sessionID string) (int64, error) {
	if sessionID != "" {
		id, ok := state.Sessions[sessionID]
		if !ok {
			return 0, fmt.Errorf("session %q not found", sessionID)
		}
		return id, nil
	}
	if len(state.Sessions) == 1 {
		for _, id := range state.Sessions {
			return id, nil
		}
	}
	if state.LastSessionID != "" {
		id, ok := state.Sessions[state.LastSessionID]
		if ok {
			return id, nil
		}
	}
	return 0, fmt.Errorf("session_id required when multiple sessions are active")
}

// writeStateLocked atomically writes state to path with mode 0600.
// Caller must hold the store's mutex.
func writeStateLocked(path string, state *ChannelState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	if _, err := f.Write(data); err != nil {
		closeErr := f.Close()
		removeErr := removeTempFile(tmpPath)
		return errors.Join(err, closeErr, removeErr)
	}
	if err := f.Close(); err != nil {
		removeErr := removeTempFile(tmpPath)
		return errors.Join(err, removeErr)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		removeErr := removeTempFile(tmpPath)
		return errors.Join(err, removeErr)
	}
	return os.Rename(tmpPath, path)
}

func removeTempFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readStateLocked(path string) (*ChannelState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ChannelState{Sessions: make(map[string]int64)}, nil
		}
		return nil, err
	}
	var state ChannelState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Sessions == nil {
		state.Sessions = make(map[string]int64)
	}
	return &state, nil
}
