package memory

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

// Service combines Store and History into a unified memory service.
type Service struct {
	store      *Store
	history    *History
	memoryDir  string
	historyDir string
}

// NewService creates a new Service, initializing directories and opening the SQLite store.
// templateFS provides embedded template files (e.g. SOUL.md, owner-profile.md) for first-run init.
func NewService(memoryDir, historyDir string, templateFS fs.FS) (*Service, error) {
	for _, dir := range []string{memoryDir, historyDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	dbPath := filepath.Join(memoryDir, "memory.db")
	store, err := NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	svc := &Service{
		store:      store,
		history:    NewHistory(historyDir),
		memoryDir:  memoryDir,
		historyDir: historyDir,
	}
	// Initialize template files on first run
	if err := svc.initializeTemplates(templateFS); err != nil {
		return nil, fmt.Errorf("initialize templates: %w", err)
	}
	return svc, nil
}

// Close closes the underlying store.
func (s *Service) Close() error {
	return s.store.Close()
}

// Read retrieves a memory entry by ID.
func (s *Service) Read(id string) (*domain.MemoryEntry, error) {
	return s.store.Get(id)
}

// BuildSessionContext returns memory context for first prompt wrapped in XML tags for better LLM attention.
// Includes SOUL (identity), owner-profile, preferences, and up to 3 recent episode titles.
func (s *Service) BuildSessionContext(ctx context.Context) (string, error) {
	var sections []string

	for _, slot := range []struct {
		id       string
		openTag  string
		closeTag string
	}{
		{"SOUL", "identity", "identity"},
		{"owner-profile", `knowledge topic="owner-profile"`, "knowledge"},
		{"preferences", `knowledge topic="preferences"`, "knowledge"},
	} {
		if section := s.appendSectionIfSubstantive(slot.id, slot.openTag, slot.closeTag); section != "" {
			sections = append(sections, section)
		}
	}

	if block := formatRecentEpisodes(s.store); block != "" {
		sections = append(sections, block)
	}

	if len(sections) == 0 {
		return "", nil
	}
	return "<memory_context>\n" +
		strings.Join(sections, "\n\n") +
		"\n</memory_context>", nil
}

func (s *Service) appendSectionIfSubstantive(id, openTag, closeTag string) string {
	entry, err := s.store.Get(id)
	if err != nil || entry == nil || !HasSubstantiveContent(entry.Content) {
		return ""
	}
	return "<" + openTag + ">\n" + entry.Content + "\n</" + closeTag + ">"
}

// Search performs full-text search across memories.
func (s *Service) Search(query, category string) ([]domain.MemoryEntry, error) {
	return s.store.Search(query, category, 5)
}

// Save writes a MemoryEntry to both the Markdown file and SQLite store.
func (s *Service) Save(entry domain.MemoryEntry) error {
	if entry.Date == "" {
		entry.Date = time.Now().Format("2006-01-02")
	}

	filePath, err := s.resolveEntryFilePath(entry)
	if err != nil {
		return err
	}
	mdContent := buildMarkdownFile(entry)
	if err := os.WriteFile(filePath, []byte(mdContent), 0o644); err != nil {
		return fmt.Errorf("write markdown file: %w", err)
	}
	return s.store.Upsert(entry)
}

// resolveEntryFilePath determines the Markdown file path from category and ensures the directory exists.
func (s *Service) resolveEntryFilePath(entry domain.MemoryEntry) (string, error) {
	var relPath string
	switch entry.Category {
	case "identity":
		relPath = entry.ID + ".md"
	case "episode":
		relPath = filepath.Join("episode", entry.ID+".md")
	default:
		relPath = filepath.Join("knowledge", entry.ID+".md")
	}
	filePath := filepath.Join(s.memoryDir, relPath)
	if entry.Category != "identity" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return "", fmt.Errorf("create dir for %s: %w", relPath, err)
		}
	}
	return filePath, nil
}

// List returns all entries, optionally filtered by category.
func (s *Service) List(category string) ([]domain.MemoryEntry, error) {
	return s.store.List(category)
}

// AppendHistory appends a message to the chat history.
func (s *Service) AppendHistory(chatID, role, text string) error {
	return s.history.Append(chatID, role, text)
}

// ReadUnsummarized reads all history content after the last summarized offset.
// chatKey should be chat.CompositeKey() (e.g. "telegram:12345").
func (s *Service) ReadUnsummarized(chatKey string) (string, error) {
	return s.history.ReadUnsummarized(chatKey)
}

// ReadUnsummarizedWithSpans reads unsummarized history and returns byte spans for each file.
func (s *Service) ReadUnsummarizedWithSpans(chatKey string) (string, []HistorySpan, error) {
	return s.history.ReadUnsummarizedWithSpans(chatKey)
}

// ReadRawHistory reads a byte range from a specific day's raw history file.
func (s *Service) ReadRawHistory(chatKey, date string, start, end int64) (string, error) {
	return s.history.ReadRawHistory(chatKey, date, start, end)
}

// MarkSummarized records the current end-of-file offsets so future reads skip them.
// chatKey should be chat.CompositeKey() (e.g. "telegram:12345").
func (s *Service) MarkSummarized(chatKey string) error {
	return s.history.MarkSummarized(chatKey)
}

// Reindex rebuilds the SQLite index from Markdown files on disk.
func (s *Service) Reindex() error {
	return s.store.Reindex(s.memoryDir)
}

const reindexInterval = 5 * time.Minute

// StartPeriodicReindex blocks until ctx is done, reindexing every reindexInterval.
// Run via errgroup: g.Go(func() error { svc.StartPeriodicReindex(gCtx); return nil })
func (s *Service) StartPeriodicReindex(ctx context.Context) {
	ticker := time.NewTicker(reindexInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reindex(); err != nil {
				slog.Warn("periodic reindex failed", "error", err)
			}
		}
	}
}

// --- helpers ---

func formatRecentEpisodes(store *Store) string {
	episodes, err := store.List("episode")
	if err != nil || len(episodes) == 0 {
		return ""
	}
	limit := min(3, len(episodes))
	lines := make([]string, limit)
	for i, ep := range episodes[:limit] {
		lines[i] = fmt.Sprintf("- %s: %s", ep.Date, ep.Title)
	}
	return "<recent_episodes>\n" + strings.Join(lines, "\n") + "\n</recent_episodes>"
}

// HasSubstantiveContent returns true if content has real data beyond template placeholders.
func HasSubstantiveContent(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "##") || strings.HasPrefix(line, "---") {
			continue
		}
		// Skip placeholder lines like "(Name, background, location, etc.)"
		if strings.HasPrefix(line, "(") && strings.HasSuffix(line, ")") {
			continue
		}
		return true
	}
	return false
}

func buildMarkdownFile(e domain.MemoryEntry) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "title: %q\n", e.Title)
	fmt.Fprintf(&sb, "date: %s\n", e.Date)
	if len(e.Tags) > 0 {
		fmt.Fprintf(&sb, "tags: [%s]\n", strings.Join(e.Tags, ", "))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(e.Content)
	return sb.String()
}

// initializeTemplates copies template files from templateFS to memory directory on first run.
func (s *Service) initializeTemplates(templateFS fs.FS) error {
	soulPath := filepath.Join(s.memoryDir, "SOUL.md")
	if _, err := os.Stat(soulPath); err == nil {
		return nil
	}
	templates := []struct {
		name string
		dest string
	}{
		{"SOUL.md", "SOUL.md"},
		{"owner-profile.md", "knowledge/owner-profile.md"},
		{"preferences.md", "knowledge/preferences.md"},
		{"people.md", "knowledge/people.md"},
		{"projects.md", "knowledge/projects.md"},
		{"notes.md", "knowledge/notes.md"},
	}
	for _, tmpl := range templates {
		data, err := fs.ReadFile(templateFS, tmpl.name)
		if err != nil {
			continue
		}
		dstPath := filepath.Join(s.memoryDir, tmpl.dest)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", tmpl.dest, err)
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return fmt.Errorf("write template %s: %w", tmpl.dest, err)
		}
	}
	return nil
}
