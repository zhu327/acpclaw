package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

const chatContextFileName = "last-context.json"

// Store persists the chat context.
type Store struct {
	contextDir string
	writeMu    sync.Mutex
}

// NewStore creates a context store.
func NewStore(contextDir string) *Store {
	return &Store{
		contextDir: contextDir,
	}
}

func (s *Store) contextFilePath() string {
	return filepath.Join(s.contextDir, chatContextFileName)
}

// Write atomically writes the most recent chat context (temp + rename).
// A mutex prevents races across concurrent writes.
func (s *Store) Write(channel, chatID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ctx := domain.SessionContext{
		Channel:   channel,
		ChatID:    chatID,
		UpdatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}

	if err := os.MkdirAll(s.contextDir, 0o700); err != nil {
		return fmt.Errorf("create context dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(s.contextDir, ".last-context.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove temp file", "path", tmpPath, "err", err)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.contextFilePath()); err != nil {
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

// Read loads the most recent chat context.
func (s *Store) Read() (*domain.SessionContext, error) {
	data, err := os.ReadFile(s.contextFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read context file: %w", err)
	}

	var ctx domain.SessionContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context file: %w", err)
	}
	return &ctx, nil
}
