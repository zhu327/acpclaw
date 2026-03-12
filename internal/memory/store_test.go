package memory_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/memory"
)

func TestStore_UpsertAndGet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath)
	require.NoError(t, err)
	defer func() {
		_ = store.Close()
	}()

	entry := memory.MemoryEntry{
		ID:       "test-1",
		Category: "knowledge",
		Title:    "Test Entry",
		Content:  "This is a test entry about Go programming.",
		Tags:     []string{"go", "test"},
		Date:     "2026-03-12",
	}
	err = store.Upsert(entry)
	require.NoError(t, err)

	got, err := store.Get("test-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "test-1", got.ID)
	assert.Equal(t, "Test Entry", got.Title)
	assert.Equal(t, []string{"go", "test"}, got.Tags)
}

func TestStore_Search(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath)
	require.NoError(t, err)
	defer func() {
		_ = store.Close()
	}()

	_ = store.Upsert(memory.MemoryEntry{
		ID: "e1", Category: "knowledge", Title: "Go Project",
		Content: "Building a Go web server with Gin", Date: "2026-03-12",
	})
	_ = store.Upsert(memory.MemoryEntry{
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
	dbPath := filepath.Join(dir, "test.db")

	knowledgeDir := filepath.Join(dir, "knowledge")
	_ = os.MkdirAll(knowledgeDir, 0o755)
	_ = os.WriteFile(filepath.Join(knowledgeDir, "notes.md"), []byte(`---
title: "Test Notes"
date: 2026-03-12
tags: [test]
---

Some test content here.
`), 0o644)

	store, err := memory.NewStore(dbPath)
	require.NoError(t, err)
	defer func() {
		_ = store.Close()
	}()

	err = store.Reindex(dir)
	require.NoError(t, err)

	got, err := store.Get("notes")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Test Notes", got.Title)
}
