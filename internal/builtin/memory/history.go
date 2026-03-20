package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HistorySpan represents the byte range of unsummarized content within a single day's file.
type HistorySpan struct {
	Date  string // "2006-01-02"
	Start int64  // byte offset (inclusive)
	End   int64  // byte offset (exclusive, i.e. file size at read time)
}

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
	content, _, err := h.ReadUnsummarizedWithSpans(chatID)
	return content, err
}

// ReadUnsummarizedWithSpans reads unsummarized content and returns spans for each file with new content.
func (h *History) ReadUnsummarizedWithSpans(chatID string) (string, []HistorySpan, error) {
	dir := filepath.Join(h.historyDir, chatID)
	offsets, err := h.loadOffsets(chatID)
	if err != nil {
		return "", nil, err
	}
	files, err := listHistoryFileNames(dir)
	if err != nil {
		return "", nil, err
	}
	if files == nil {
		return "", nil, nil
	}

	var sb strings.Builder
	var spans []HistorySpan
	for _, fname := range files {
		offset := offsets[fname]
		path := filepath.Join(dir, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("failed to read history file", "path", path, "error", err)
			continue
		}
		size := int64(len(data))
		if offset >= size {
			continue
		}
		sb.Write(data[offset:])
		date := strings.TrimSuffix(fname, ".txt")
		spans = append(spans, HistorySpan{Date: date, Start: offset, End: size})
	}
	return sb.String(), spans, nil
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

// ReadRawHistory reads a byte range [start, end) from a specific day's history file.
// Returns an error if the requested range exceeds 1MB to prevent unbounded reads.
func (h *History) ReadRawHistory(chatID, date string, start, end int64) (string, error) {
	path := filepath.Clean(filepath.Join(h.historyDir, chatID, date+".txt"))
	if !strings.HasPrefix(path, filepath.Clean(h.historyDir)+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid chat_key or date: path escape detected")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open history file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("failed to close history file", "error", err)
		}
	}()

	length := end - start
	if length <= 0 {
		return "", nil
	}
	if length > 1<<20 {
		return "", fmt.Errorf("requested range too large: %d bytes", length)
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}

	buf := make([]byte, int(length))
	read, err := io.ReadFull(f, buf)
	switch {
	case err == nil:
		return string(buf), nil
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return string(buf[:read]), nil
	default:
		return "", fmt.Errorf("read: %w", err)
	}
}

// InsertRawReferences 将 raw_references 插入 content 中已有的 YAML front matter。
// 如果 content 没有 front matter 或无 spans，则原样返回。
func InsertRawReferences(content, chatKey string, spans []HistorySpan) string {
	if len(spans) == 0 {
		return content
	}
	closeAt, ok := closingYAMLDelimiter(content)
	if !ok {
		return content
	}

	var sb strings.Builder
	sb.WriteString("raw_references:\n")
	for _, span := range spans {
		fmt.Fprintf(&sb, "  - chat_key=%s, date=%s, start_offset=%d, end_offset=%d\n",
			chatKey, span.Date, span.Start, span.End)
	}

	prefix := content[:closeAt]
	if !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + sb.String() + content[closeAt:]
}

func listHistoryFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
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
