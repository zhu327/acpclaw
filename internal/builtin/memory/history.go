package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// History manages per-chat conversation history files.
type History struct {
	historyDir string
}

// NewHistory creates a History manager rooted at historyDir.
func NewHistory(historyDir string) *History {
	return &History{historyDir: historyDir}
}

// Append appends a message to today's history file for the given chatID.
func (h *History) Append(chatID, role, text string) error {
	dir := filepath.Join(h.historyDir, chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, today+".txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close history file: %v\n", err)
		}
	}()
	_, err = fmt.Fprintf(f, "[%s] %s\n\n", role, text)
	return err
}

// ReadUnsummarized reads all history content after the last summarized offset.
func (h *History) ReadUnsummarized(chatID string) (string, error) {
	dir := filepath.Join(h.historyDir, chatID)
	offsets, err := h.loadOffsets(chatID)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read history dir: %w", err)
	}

	// Collect .txt files sorted by name (date order)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var sb strings.Builder
	for _, fname := range files {
		offset := offsets[fname]
		path := filepath.Join(dir, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if offset >= int64(len(data)) {
			continue
		}
		sb.Write(data[offset:])
	}
	return sb.String(), nil
}

// MarkSummarized records the current end-of-file offsets so future reads skip them.
func (h *History) MarkSummarized(chatID string) error {
	dir := filepath.Join(h.historyDir, chatID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read history dir: %w", err)
	}

	offsets := make(map[string]int64)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		offsets[e.Name()] = info.Size()
	}

	data, err := json.Marshal(offsets)
	if err != nil {
		return fmt.Errorf("marshal offsets: %w", err)
	}
	offsetPath := filepath.Join(dir, ".last-summarized-offset")
	return os.WriteFile(offsetPath, data, 0o644)
}

func (h *History) loadOffsets(chatID string) (map[string]int64, error) {
	offsetPath := filepath.Join(h.historyDir, chatID, ".last-summarized-offset")
	data, err := os.ReadFile(offsetPath)
	if os.IsNotExist(err) {
		return make(map[string]int64), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read offset file: %w", err)
	}
	var offsets map[string]int64
	if err := json.Unmarshal(data, &offsets); err != nil {
		return make(map[string]int64), nil
	}
	return offsets, nil
}
