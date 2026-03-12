package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const chatContextFileName = "last-context.json"

// Context 存储当前会话的上下文信息
type Context struct {
	Channel   string    `json:"channel"`
	ChatID    string    `json:"chatId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store 负责会话上下文的持久化
type Store struct {
	contextDir string
	writeMu    sync.Mutex
}

// NewStore 创建会话上下文存储
func NewStore(contextDir string) *Store {
	return &Store{
		contextDir: contextDir,
	}
}

func (s *Store) contextFilePath() string {
	return filepath.Join(s.contextDir, chatContextFileName)
}

// Write 原子写入最近活跃的 chat 上下文（temp + rename 模式）
// 使用互斥锁防止多个并发调用时的竞态条件
func (s *Store) Write(channel, chatID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ctx := Context{
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

// Read 读取最近的 chat 上下文
func (s *Store) Read() (*Context, error) {
	data, err := os.ReadFile(s.contextFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read context file: %w", err)
	}

	var ctx Context
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context file: %w", err)
	}
	return &ctx, nil
}
