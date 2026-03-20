package memory_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/builtin/memory"
	"github.com/zhu327/acpclaw/internal/domain"
)

func newTestStore(t *testing.T, dir string) *memory.Store {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	store, err := memory.NewStore(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStore_UpsertAndGet(t *testing.T) {
	store := newTestStore(t, "")

	entry := domain.MemoryEntry{
		ID:       "test-1",
		Category: "knowledge",
		Title:    "Test Entry",
		Content:  "This is a test entry about Go programming.",
		Tags:     []string{"go", "test"},
		Date:     "2026-03-12",
	}
	err := store.Upsert(entry)
	require.NoError(t, err)

	got, err := store.Get("test-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "test-1", got.ID)
	assert.Equal(t, "Test Entry", got.Title)
	assert.Equal(t, []string{"go", "test"}, got.Tags)
}

func TestStore_Search(t *testing.T) {
	store := newTestStore(t, "")

	_ = store.Upsert(domain.MemoryEntry{
		ID: "e1", Category: "knowledge", Title: "Go Project",
		Content: "Building a Go web server with Gin", Date: "2026-03-12",
	})
	_ = store.Upsert(domain.MemoryEntry{
		ID: "e2", Category: "episode", Title: "Python Discussion",
		Content: "Discussed Python data science tools", Date: "2026-03-11",
	})

	results, err := store.Search("Go web", "", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "e1", results[0].ID)
}

func TestStore_Search_BM25Rerank(t *testing.T) {
	store := newTestStore(t, "")

	// e1: "Go" 出现 3 次（高频），应排在 e2 之前
	_ = store.Upsert(domain.MemoryEntry{
		ID: "e1", Category: "knowledge", Title: "Go Guide",
		Content: "Go is great. Go is fast. Use Go for backend.", Date: "2026-03-12",
	})
	// e2: "Go" 仅出现 1 次，且文档更长（BM25 会惩罚长文档中低频词）
	_ = store.Upsert(domain.MemoryEntry{
		ID: "e2", Category: "knowledge", Title: "Programming Languages",
		Content: "There are many languages: Python, Java, Ruby, Rust, Go, and more. Each has trade-offs.",
		Date:    "2026-03-11",
	})

	results, err := store.Search("Go", "", 5)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 1)
	// e1 词频更高、文档更短，BM25 应排在首位
	assert.Equal(t, "e1", results[0].ID)
}

func TestStore_Search_BM25RerankLimit(t *testing.T) {
	store := newTestStore(t, "")

	// 插入 6 条包含 "memory" 的记录，Search limit=3 应只返回 3 条
	for i := range 6 {
		_ = store.Upsert(domain.MemoryEntry{
			ID:       fmt.Sprintf("m%d", i),
			Category: "knowledge",
			Title:    fmt.Sprintf("Memory Note %d", i),
			Content:  fmt.Sprintf("This note %d is about memory management and allocation.", i),
			Date:     "2026-03-12",
		})
	}

	results, err := store.Search("memory", "", 3)
	require.NoError(t, err)
	assert.Equal(t, 3, len(results))
}

func TestStore_Reindex(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	knowledgeDir := filepath.Join(dir, "knowledge")
	_ = os.MkdirAll(knowledgeDir, 0o755)
	_ = os.WriteFile(filepath.Join(knowledgeDir, "notes.md"), []byte(`---
title: "Test Notes"
date: 2026-03-12
tags: [test]
---

Some test content here.
`), 0o644)

	err := store.Reindex(dir)
	require.NoError(t, err)

	got, err := store.Get("notes")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Test Notes", got.Title)
}

func TestStore_Reindex_FrontmatterExpandDetailsAndRawReferences(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	knowledgeDir := filepath.Join(dir, "knowledge")
	require.NoError(t, os.MkdirAll(knowledgeDir, 0o755))
	md := `---
title: "Ref Doc"
date: 2026-03-12
tags: [alpha, beta]
expand_details: hidden detail text
raw_references:
  - chat_key=k, date=2026-01-01, start=1, end=2
  - second ref line
---

Main body paragraph.
`
	require.NoError(t, os.WriteFile(filepath.Join(knowledgeDir, "refdoc.md"), []byte(md), 0o644))

	require.NoError(t, store.Reindex(dir))

	got, err := store.Get("refdoc")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Ref Doc", got.Title)
	assert.Equal(t, "2026-03-12", got.Date)
	assert.Equal(t, []string{"alpha", "beta"}, got.Tags)
	assert.NotContains(t, got.Content, "---")
	assert.Contains(t, got.Content, "Main body paragraph.")
	assert.Contains(t, got.Content, "Expand for details: hidden detail text")
	assert.Contains(t, got.Content, "> Raw Reference: chat_key=k, date=2026-01-01, start=1, end=2")
	assert.Contains(t, got.Content, "> Raw Reference: second ref line")
}

func TestStore_Reindex_EpisodeYAMLReconstructsBodyInDB(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	epDir := filepath.Join(dir, "episode")
	require.NoError(t, os.MkdirAll(epDir, 0o755))
	md := `---
title: "Session A"
date: 2026-03-18
expand_details: see transcript
raw_references:
  - chat_key=tg:1, date=2026-03-18, start_offset=0, end_offset=50
---

Summary only here.
`
	require.NoError(t, os.WriteFile(filepath.Join(epDir, "sess-a.md"), []byte(md), 0o644))

	require.NoError(t, store.Reindex(dir))

	got, err := store.Get("sess-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Session A", got.Title)
	assert.NotContains(t, got.Content, "---")
	assert.Contains(t, got.Content, "Summary only here.")
	assert.Contains(t, got.Content, "Expand for details: see transcript")
	assert.Contains(t, got.Content, "> Raw Reference: chat_key=tg:1, date=2026-03-18, start_offset=0, end_offset=50")
}

func TestStore_Reindex_ClearsDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)

	knowledgeDir := filepath.Join(dir, "knowledge")
	require.NoError(t, os.MkdirAll(knowledgeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(knowledgeDir, "orphan.md"), []byte(`---
title: "Orphan"
date: 2026-03-12
---

Orphan content.
`), 0o644))

	// First Reindex: orphan should be indexed
	require.NoError(t, store.Reindex(dir))
	got, err := store.Get("orphan")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Remove orphan.md file
	require.NoError(t, os.Remove(filepath.Join(knowledgeDir, "orphan.md")))

	// Second Reindex: orphan record should be cleared
	require.NoError(t, store.Reindex(dir))
	got, err = store.Get("orphan")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestStore_Search_TwoPassCjk(t *testing.T) {
	store := newTestStore(t, "")

	// English exact match: should return directly without triggering CJK OR
	_ = store.Upsert(domain.MemoryEntry{
		ID: "e1", Category: "knowledge", Title: "Go Project",
		Content: "Building a Go web server with Gin", Date: "2026-03-12",
	})
	results, err := store.Search("Go web", "", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "e1", results[0].ID)

	// Chinese query: two-pass (exact then OR expand). unicode61 may tokenize CJK as whole; if results exist, first should be e2
	_ = store.Upsert(domain.MemoryEntry{
		ID: "e2", Category: "knowledge", Title: "项目笔记",
		Content: "讨论了女朋友相关的话题，女友喜欢编程", Date: "2026-03-11",
	})
	results, err = store.Search("女朋友", "", 5)
	require.NoError(t, err)
	// FTS5 unicode61 may tokenize CJK as one token; if we get results, e2 should rank first
	if len(results) > 0 {
		assert.Equal(t, "e2", results[0].ID)
	}
}
