package memory

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
	_ "modernc.org/sqlite" // SQLite driver
)

// BM25 参数
const (
	bm25K1 = 1.2  // 词频饱和参数
	bm25B  = 0.75 // 文档长度归一化参数
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

// Search performs FTS5 full-text search with BM25 reranking.
// Strategy:
// - Pass 1: FTS5 retrieves a candidate pool (max(10, limit×2))
// - Pass 2: if results < candidateSize and query contains CJK, expand with OR-expanded tokens
// - BM25 rerank: rerank candidates by BM25 score, return top limit
func (s *Store) Search(query, category string, limit int) ([]domain.MemoryEntry, error) {
	if limit <= 0 {
		limit = 5
	}

	// FTS5 候选池大小：至少 10，最多 limit×2
	candidateSize := max(10, limit*2)

	// Pass 1: FTS5 召回候选集；FTS 语法错误时返回空（与 NeoClaw 对齐）
	candidates, err := s.ftsSearch(query, category, candidateSize)
	if err != nil {
		if isFtsSyntaxError(err) {
			return nil, nil
		}
		return nil, err
	}

	// Pass 2: CJK OR fallback 补充候选
	if more := s.searchCjkFallback(candidates, query, category, candidateSize); more != nil {
		candidates = appendDeduplicated(candidates, more)
	}

	// BM25 重排序，从候选集中返回 top limit
	return bm25Rerank(candidates, query, limit), nil
}

// searchCjkFallback runs OR-expanded CJK search when pass 1 returned fewer than limit results.
// Returns nil if fallback is skipped or fails (caller keeps pass 1 results).
func (s *Store) searchCjkFallback(
	existing []domain.MemoryEntry,
	query, category string,
	limit int,
) []domain.MemoryEntry {
	if len(existing) >= limit || !cjkRe.MatchString(query) {
		return nil
	}
	orQuery := buildCjkOrQuery(query)
	if orQuery == query {
		return nil
	}
	more, err := s.ftsSearch(orQuery, category, limit-len(existing))
	if err != nil {
		return nil
	}
	return more
}

func appendDeduplicated(a, b []domain.MemoryEntry) []domain.MemoryEntry {
	seen := make(map[string]bool)
	for _, r := range a {
		seen[r.ID] = true
	}
	for _, r := range b {
		if !seen[r.ID] {
			a = append(a, r)
			seen[r.ID] = true
		}
	}
	return a
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

// Reindex rebuilds the index from Markdown files in memoryDir.
// It first clears all existing records, then scans: <memoryDir>/SOUL.md,
// <memoryDir>/knowledge/*.md, <memoryDir>/episode/*.md
func (s *Store) Reindex(memoryDir string) error {
	// Clear first, then rebuild; aligns with NeoClaw to avoid orphan records from deleted files.
	if _, err := s.db.Exec("DELETE FROM memory"); err != nil {
		return fmt.Errorf("clear memory table: %w", err)
	}

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

var ftsSyntaxErrorSubstrings = []string{"fts5", "syntax", "malformed"}

// isFtsSyntaxError returns true if err appears to be an FTS5 query syntax error.
func isFtsSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sub := range ftsSyntaxErrorSubstrings {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

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

// closingYAMLDelimiter returns the index in content where the closing "---" of YAML front matter starts.
// The opening delimiter must be at index 0.
func closingYAMLDelimiter(content string) (at int, ok bool) {
	if !strings.HasPrefix(content, "---") {
		return 0, false
	}
	rel := strings.Index(content[3:], "---")
	if rel < 0 {
		return 0, false
	}
	return 3 + rel, true
}

// parseFrontmatter extracts title, date, tags and body from YAML frontmatter.
// 同时解析 expand_details 和 raw_references 字段，并还原到 body 末尾以保持 DB 兼容性。
func parseFrontmatter(content string) (title, date string, tags []string, body string) {
	body = content
	closeAt, ok := closingYAMLDelimiter(content)
	if !ok {
		return
	}
	front := content[3:closeAt]
	body = strings.TrimSpace(content[closeAt+3:])

	var expandDetails string
	var rawRefLines []string
	inRawRefs := false

	for _, line := range strings.Split(front, "\n") {
		trimmed := strings.TrimSpace(line)
		if inRawRefs {
			if strings.HasPrefix(trimmed, "- ") {
				rawRefLines = append(rawRefLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
				continue
			}
			if trimmed == "" {
				continue
			}
			inRawRefs = false
		}
		if strings.HasPrefix(trimmed, "title:") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "title:"))
			title = strings.Trim(title, `"'`)
		} else if strings.HasPrefix(trimmed, "date:") {
			date = strings.TrimSpace(strings.TrimPrefix(trimmed, "date:"))
			date = strings.Trim(date, `"'`)
		} else if strings.HasPrefix(trimmed, "tags:") {
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "tags:"))
			raw = strings.Trim(raw, "[]")
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				t = strings.Trim(t, `"'`)
				if t != "" {
					tags = append(tags, t)
				}
			}
		} else if strings.HasPrefix(trimmed, "expand_details:") {
			expandDetails = strings.TrimSpace(strings.TrimPrefix(trimmed, "expand_details:"))
			expandDetails = strings.Trim(expandDetails, `"'`)
		} else if trimmed == "raw_references:" {
			inRawRefs = true
		}
	}

	if expandDetails != "" {
		body += "\n\nExpand for details: " + expandDetails
	}
	for _, ref := range rawRefLines {
		body += "\n\n> Raw Reference: " + ref
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

// isBm25TokenChar 判断字符是否参与 BM25 分词（小写字母、数字、CJK）。
func isBm25TokenChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
		(r >= '\u4e00' && r <= '\u9fff') || (r >= '\u3400' && r <= '\u4dbf')
}

// bm25Tokenize 将文本分词为小写词元（与 opencode BM25 实现对齐）。
func bm25Tokenize(text string) []string {
	var buf strings.Builder
	for _, ch := range strings.ToLower(text) {
		if isBm25TokenChar(ch) {
			buf.WriteRune(ch)
		} else {
			buf.WriteRune(' ')
		}
	}
	var tokens []string
	for _, t := range strings.Fields(buf.String()) {
		if len([]rune(t)) > 1 {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// bm25Score 计算单个文档相对查询词的 BM25 得分。
// termFreq: 文档词频表，docLen: 文档长度，
// avgDocLen: 所有文档平均长度，docCount: 文档总数，dfMap: 词→含该词的文档数。
func bm25Score(
	queryTerms []string,
	termFreq map[string]int,
	docLen, avgDocLen float64,
	docCount int,
	dfMap map[string]int,
) float64 {
	var score float64
	for _, term := range queryTerms {
		tf := float64(termFreq[term])
		if tf == 0 {
			continue
		}
		n := float64(dfMap[term])
		N := float64(docCount)
		// BM25 IDF 公式（Robertson & Sparck Jones）
		idf := math.Log((N-n+0.5)/(n+0.5) + 1)
		// BM25 TF 归一化
		numerator := tf * (bm25K1 + 1)
		denominator := tf + bm25K1*(1-bm25B+bm25B*(docLen/avgDocLen))
		score += idf * (numerator / denominator)
	}
	return score
}

// docInfo 文档 BM25 计算所需信息
type docInfo struct {
	entry    domain.MemoryEntry
	termFreq map[string]int
	docLen   int
}

// scored 带 BM25 分数的条目
type scored struct {
	entry domain.MemoryEntry
	score float64
}

// bm25Rerank 对 FTS5 候选集用 BM25 重排序，返回 top limit 条。
func bm25Rerank(candidates []domain.MemoryEntry, query string, limit int) []domain.MemoryEntry {
	if len(candidates) <= limit {
		return candidates
	}
	queryTerms := bm25Tokenize(query)
	if len(queryTerms) == 0 {
		return candidates[:limit]
	}

	docs := make([]docInfo, len(candidates))
	dfMap := make(map[string]int, 64)
	var totalLen int

	for i, e := range candidates {
		tokens := bm25Tokenize(e.Title + " " + e.Content)
		freq := make(map[string]int, len(tokens))
		for _, t := range tokens {
			freq[t]++
		}
		for t := range freq {
			dfMap[t]++
		}
		docs[i] = docInfo{entry: e, termFreq: freq, docLen: len(tokens)}
		totalLen += len(tokens)
	}

	avgDocLen := float64(totalLen) / float64(len(docs))
	docCount := len(docs)
	ranked := make([]scored, len(docs))
	for i, d := range docs {
		ranked[i] = scored{
			entry: d.entry,
			score: bm25Score(queryTerms, d.termFreq, float64(d.docLen), avgDocLen, docCount, dfMap),
		}
	}

	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	top := min(limit, len(ranked))
	result := make([]domain.MemoryEntry, top)
	for i := range top {
		result[i] = ranked[i].entry
	}
	return result
}
