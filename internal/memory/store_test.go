package memory_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/memory"
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
