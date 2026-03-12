package memory

import (
	"context"
	"fmt"
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
func NewService(memoryDir, historyDir string) (*Service, error) {
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
	if err := svc.initializeTemplates(); err != nil {
		return nil, fmt.Errorf("initialize templates: %w", err)
	}
	return svc, nil
}

// Close closes the underlying store.
func (s *Service) Close() error {
	return s.store.Close()
}

// Read retrieves a memory entry by ID.
func (s *Service) Read(id string) (*MemoryEntry, error) {
	return s.store.Get(id)
}

// Search performs full-text search across memories.
func (s *Service) Search(query, category string) ([]MemoryEntry, error) {
	return s.store.Search(query, category, 5)
}

// Save writes a MemoryEntry to both the Markdown file and SQLite store.
func (s *Service) Save(entry MemoryEntry) error {
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
func (s *Service) resolveEntryFilePath(entry MemoryEntry) (string, error) {
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
func (s *Service) List(category string) ([]MemoryEntry, error) {
	return s.store.List(category)
}

// AppendHistory appends a message to the chat history.
func (s *Service) AppendHistory(chatID, role, text string) error {
	return s.history.Append(chatID, role, text)
}

const (
	minTranscriptLenForSummarize = 100
	maxTranscriptLenForSummarize = 50000
)

// SummarizeSession reads unsummarized history, prompts the agent for a summary, and saves an episode.
func (s *Service) SummarizeSession(ctx context.Context, chatID string, summarizer domain.Summarizer) error {
	if summarizer == nil {
		return nil
	}

	transcript, err := s.history.ReadUnsummarized(chatID)
	if err != nil || len(transcript) < minTranscriptLenForSummarize {
		return nil // Conversation too short; skip.
	}
	if len(transcript) > maxTranscriptLenForSummarize {
		transcript = transcript[len(transcript)-maxTranscriptLenForSummarize:]
	}

	summary, err := summarizer.Summarize(ctx, transcript)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}
	if summary == "" {
		return nil
	}

	entry := MemoryEntry{
		ID:       buildEpisodeFileName(chatID),
		Category: "episode",
		Title:    extractTitle(summary),
		Content:  summary,
		Date:     time.Now().Format("2006-01-02"),
	}
	if err := s.Save(entry); err != nil {
		return fmt.Errorf("save episode: %w", err)
	}
	return s.history.MarkSummarized(chatID)
}

// sanitizeForFilename removes non-alphanumeric characters to produce a safe filename.
func sanitizeForFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, s)
}

func buildEpisodeFileName(chatID string) string {
	return fmt.Sprintf("%s_%s_%d",
		time.Now().Format("2006-01-02"),
		sanitizeForFilename(chatID),
		time.Now().Unix())
}

// Reindex rebuilds the SQLite index from Markdown files on disk.
func (s *Service) Reindex() error {
	return s.store.Reindex(s.memoryDir)
}

// --- helpers ---

func buildMarkdownFile(e MemoryEntry) string {
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

func extractTitle(text string) string {
	// Try to extract title from frontmatter
	title, _, _, _ := parseFrontmatter(text)
	if title != "" {
		return title
	}
	// Fallback: use first non-empty line
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "---") {
			if len(line) > 60 {
				line = line[:60]
			}
			return line
		}
	}
	return "Session Summary"
}

// initializeTemplates copies template files to memory directory on first run.
func (s *Service) initializeTemplates() error {
	// Check if SOUL.md already exists (indicates this is not first run)
	soulPath := filepath.Join(s.memoryDir, "SOUL.md")
	if _, err := os.Stat(soulPath); err == nil {
		return nil // Templates already exist
	}

	// Get the executable directory to find templates folder
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	templatesDir := filepath.Join(filepath.Dir(exe), "..", "templates")

	// If templates directory doesn't exist, try relative to current working directory
	if _, err := os.Stat(templatesDir); os.IsNotExist(err) {
		if cwd, err := os.Getwd(); err == nil {
			templatesDir = filepath.Join(cwd, "templates")
		}
	}

	// If templates directory still doesn't exist, skip initialization (not an error)
	if _, err := os.Stat(templatesDir); os.IsNotExist(err) {
		return nil
	}

	// Copy template files
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
		srcPath := filepath.Join(templatesDir, tmpl.name)
		dstPath := filepath.Join(s.memoryDir, tmpl.dest)

		// Ensure destination directory exists
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", tmpl.dest, err)
		}

		// Read template file
		data, err := os.ReadFile(srcPath)
		if err != nil {
			// If template doesn't exist, skip it (not critical)
			continue
		}

		// Write to destination
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return fmt.Errorf("write template %s: %w", tmpl.dest, err)
		}
	}

	return nil
}
