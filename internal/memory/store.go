package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
	_ "modernc.org/sqlite" // SQLite driver
)

// Store wraps a SQLite database for memory persistence.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS memory (
    id       TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    title    TEXT NOT NULL,
    content  TEXT NOT NULL,
    tags     TEXT NOT NULL DEFAULT '',
    date     TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
    id, category, title, content, tags, date,
    content='memory',
    content_rowid='rowid',
    tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memory BEGIN
    INSERT INTO memory_fts(rowid, id, category, title, content, tags, date)
        VALUES (new.rowid, new.id, new.category, new.title, new.content, new.tags, new.date);
END;

CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memory BEGIN
    INSERT INTO memory_fts(memory_fts, rowid, id, category, title, content, tags, date)
        VALUES ('delete', old.rowid, old.id, old.category, old.title, old.content, old.tags, old.date);
END;

CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memory BEGIN
    INSERT INTO memory_fts(memory_fts, rowid, id, category, title, content, tags, date)
        VALUES ('delete', old.rowid, old.id, old.category, old.title, old.content, old.tags, old.date);
    INSERT INTO memory_fts(rowid, id, category, title, content, tags, date)
        VALUES (new.rowid, new.id, new.category, new.title, new.content, new.tags, new.date);
END;
`

// NewStore opens (or creates) a SQLite database at dbPath and initializes the schema.
func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert inserts or replaces a memory entry.
func (s *Store) Upsert(e domain.MemoryEntry) error {
	tags := strings.Join(e.Tags, ",")
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO memory (id, category, title, content, tags, date) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.Category, e.Title, e.Content, tags, e.Date,
	)
	return err
}

// Get retrieves a memory entry by ID. Returns nil if not found.
func (s *Store) Get(id string) (*domain.MemoryEntry, error) {
	row := s.db.QueryRow(
		`SELECT id, category, title, content, tags, date FROM memory WHERE id = ?`, id,
	)
	return scanEntry(row)
}

// Search performs FTS5 full-text search with optional category filter.
// CJK characters are expanded to OR tokens for better matching.
func (s *Store) Search(query, category string, limit int) ([]domain.MemoryEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	ftsQuery := buildCjkOrQuery(query)
	return s.ftsSearch(ftsQuery, category, limit)
}

func (s *Store) ftsSearch(ftsQuery, category string, limit int) ([]domain.MemoryEntry, error) {
	query := ftsSearchQuery(category)
	args := []any{ftsQuery}
	if category != "" {
		args = append(args, category)
	}
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	return scanEntries(rows)
}

func ftsSearchQuery(category string) string {
	if category != "" {
		return `SELECT m.id, m.category, m.title, m.content, m.tags, m.date
			FROM memory_fts f
			JOIN memory m ON m.rowid = f.rowid
			WHERE memory_fts MATCH ? AND m.category = ?
			ORDER BY rank LIMIT ?`
	}
	return `SELECT m.id, m.category, m.title, m.content, m.tags, m.date
		FROM memory_fts f
		JOIN memory m ON m.rowid = f.rowid
		WHERE memory_fts MATCH ?
		ORDER BY rank LIMIT ?`
}

// List returns all entries, optionally filtered by category.
func (s *Store) List(category string) ([]domain.MemoryEntry, error) {
	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = s.db.Query(
			`SELECT id, category, title, content, tags, date FROM memory WHERE category = ? ORDER BY date DESC`,
			category,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, category, title, content, tags, date FROM memory ORDER BY date DESC`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	return scanEntries(rows)
}

// Delete removes a memory entry by ID.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM memory WHERE id = ?`, id)
	return err
}

// Reindex rebuilds the index from Markdown files in memoryDir.
// It scans: <memoryDir>/SOUL.md, <memoryDir>/knowledge/*.md, <memoryDir>/episode/*.md
func (s *Store) Reindex(memoryDir string) error {
	// identity: SOUL.md
	soulPath := filepath.Join(memoryDir, "SOUL.md")
	if data, err := os.ReadFile(soulPath); err == nil {
		entry := parseMarkdownFile("SOUL", "identity", string(data))
		if err := s.Upsert(entry); err != nil {
			return fmt.Errorf("upsert SOUL: %w", err)
		}
	}

	// knowledge/*.md
	if err := s.reindexDir(memoryDir, "knowledge"); err != nil {
		return err
	}
	// episodes/*.md
	if err := s.reindexDir(memoryDir, "episode"); err != nil {
		return err
	}
	return nil
}

func (s *Store) reindexDir(memoryDir, category string) error {
	dir := filepath.Join(memoryDir, category)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(de.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		entry := parseMarkdownFile(id, category, string(data))
		if err := s.Upsert(entry); err != nil {
			return fmt.Errorf("upsert %s/%s: %w", category, id, err)
		}
	}
	return nil
}

// --- helpers ---

func scanEntry(row *sql.Row) (*domain.MemoryEntry, error) {
	var e domain.MemoryEntry
	var tags string
	err := row.Scan(&e.ID, &e.Category, &e.Title, &e.Content, &tags, &e.Date)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Tags = splitTags(tags)
	return &e, nil
}

func scanEntries(rows *sql.Rows) ([]domain.MemoryEntry, error) {
	var results []domain.MemoryEntry
	for rows.Next() {
		var e domain.MemoryEntry
		var tags string
		if err := rows.Scan(&e.ID, &e.Category, &e.Title, &e.Content, &tags, &e.Date); err != nil {
			return nil, err
		}
		e.Tags = splitTags(tags)
		results = append(results, e)
	}
	return results, rows.Err()
}

func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	parts := strings.Split(tags, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// parseMarkdownFile extracts a MemoryEntry from a Markdown file with optional YAML frontmatter.
func parseMarkdownFile(id, category, content string) domain.MemoryEntry {
	entry := domain.MemoryEntry{
		ID:       id,
		Category: category,
		Title:    id,
		Content:  content,
	}
	title, date, tags, body := parseFrontmatter(content)
	if title != "" {
		entry.Title = title
	}
	if date != "" {
		entry.Date = date
	}
	entry.Tags = tags
	entry.Content = body
	return entry
}

// parseFrontmatter extracts title, date, tags and body from YAML frontmatter.
func parseFrontmatter(content string) (title, date string, tags []string, body string) {
	body = content
	if !strings.HasPrefix(content, "---") {
		return
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return
	}
	front := content[3 : end+3]
	body = strings.TrimSpace(content[end+6:])

	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "title:") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "title:"))
			title = strings.Trim(title, `"'`)
		} else if strings.HasPrefix(line, "date:") {
			date = strings.TrimSpace(strings.TrimPrefix(line, "date:"))
			date = strings.Trim(date, `"'`)
		} else if strings.HasPrefix(line, "tags:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
			// Support both [a, b] and "a, b" formats
			raw = strings.Trim(raw, "[]")
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				t = strings.Trim(t, `"'`)
				if t != "" {
					tags = append(tags, t)
				}
			}
		}
	}
	return
}

// CJK character range regexp.
var cjkRe = regexp.MustCompile(`[\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}]`)

// buildCjkOrQuery expands CJK characters into OR tokens for FTS5.
func buildCjkOrQuery(raw string) string {
	if !cjkRe.MatchString(raw) {
		return raw
	}
	var tokens []string
	var buf strings.Builder
	for _, ch := range raw {
		if cjkRe.MatchString(string(ch)) {
			if buf.Len() > 0 {
				tokens = append(tokens, strings.Fields(buf.String())...)
				buf.Reset()
			}
			tokens = append(tokens, string(ch))
		} else {
			buf.WriteRune(ch)
		}
	}
	if buf.Len() > 0 {
		tokens = append(tokens, strings.Fields(buf.String())...)
	}
	if len(tokens) <= 1 {
		return raw
	}
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " OR ")
}
