package mcp

import (
	"context"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

// mockMemoryStore is an in-memory implementation of MemoryStore for testing.
type mockMemoryStore struct {
	entries map[string]domain.MemoryEntry
}

func newMockMemoryStore() *mockMemoryStore {
	return &mockMemoryStore{entries: make(map[string]domain.MemoryEntry)}
}

func (m *mockMemoryStore) Read(id string) (*domain.MemoryEntry, error) {
	if e, ok := m.entries[id]; ok {
		return &e, nil
	}
	return nil, nil
}

func (m *mockMemoryStore) Search(query, category string) ([]domain.MemoryEntry, error) {
	var out []domain.MemoryEntry
	for _, e := range m.entries {
		if category != "" && e.Category != category {
			continue
		}
		if strings.Contains(strings.ToLower(e.Content), strings.ToLower(query)) ||
			strings.Contains(strings.ToLower(e.Title), strings.ToLower(query)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *mockMemoryStore) Save(entry domain.MemoryEntry) error {
	m.entries[entry.ID] = entry
	return nil
}

func (m *mockMemoryStore) List(category string) ([]domain.MemoryEntry, error) {
	var out []domain.MemoryEntry
	for _, e := range m.entries {
		if category == "" || e.Category == category {
			out = append(out, e)
		}
	}
	return out, nil
}

func makeMemoryRequest(name string, args map[string]any) mcplib.CallToolRequest {
	return mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func TestMemoryReadHandler(t *testing.T) {
	store := newMockMemoryStore()
	require.NoError(t, store.Save(domain.MemoryEntry{
		ID: "prefs", Category: "knowledge", Title: "Preferences", Content: "Dark mode", Date: "2026-01-01",
	}))
	handler := memoryReadHandler(store)
	ctx := context.Background()

	t.Run("missing id", func(t *testing.T) {
		req := makeMemoryRequest("memory_read", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "id is required")
	})

	t.Run("not found", func(t *testing.T) {
		req := makeMemoryRequest("memory_read", map[string]any{"id": "nonexistent"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "no memory found")
	})

	t.Run("found", func(t *testing.T) {
		req := makeMemoryRequest("memory_read", map[string]any{"id": "prefs"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		text := res.Content[0].(mcplib.TextContent).Text
		assert.Contains(t, text, "id: prefs")
		assert.Contains(t, text, "Dark mode")
	})
}

func TestMemorySearchHandler(t *testing.T) {
	store := newMockMemoryStore()
	require.NoError(t, store.Save(domain.MemoryEntry{
		ID: "a", Category: "knowledge", Title: "A", Content: "hello world", Date: "2026-01-01",
	}))
	require.NoError(t, store.Save(domain.MemoryEntry{
		ID: "b", Category: "knowledge", Title: "B", Content: "goodbye", Date: "2026-01-01",
	}))
	handler := memorySearchHandler(store)
	ctx := context.Background()

	t.Run("missing query", func(t *testing.T) {
		req := makeMemoryRequest("memory_search", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "query is required")
	})

	t.Run("no matches", func(t *testing.T) {
		req := makeMemoryRequest("memory_search", map[string]any{"query": "xyz"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "No matching memories")
	})

	t.Run("matches", func(t *testing.T) {
		req := makeMemoryRequest("memory_search", map[string]any{"query": "hello"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		text := res.Content[0].(mcplib.TextContent).Text
		assert.Contains(t, text, "hello world")
	})
}

func TestMemorySaveHandler(t *testing.T) {
	store := newMockMemoryStore()
	handler := memorySaveHandler(store)
	ctx := context.Background()

	t.Run("missing content", func(t *testing.T) {
		req := makeMemoryRequest("memory_save", map[string]any{"id": "preferences"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "content is required")
	})

	t.Run("identity save", func(t *testing.T) {
		req := makeMemoryRequest("memory_save", map[string]any{
			"content":  "# Soul\nValues here",
			"category": "identity",
		})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "Saved")
		entry, _ := store.Read("SOUL")
		require.NotNil(t, entry)
		assert.Equal(t, "identity", entry.Category)
	})

	t.Run("knowledge save", func(t *testing.T) {
		req := makeMemoryRequest("memory_save", map[string]any{
			"id": "preferences", "content": "Dark mode", "category": "knowledge",
		})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		entry, _ := store.Read("preferences")
		require.NotNil(t, entry)
		assert.Equal(t, "Dark mode", entry.Content)
	})
}

func TestMemoryListHandler(t *testing.T) {
	store := newMockMemoryStore()
	handler := memoryListHandler(store)
	ctx := context.Background()

	t.Run("empty", func(t *testing.T) {
		req := makeMemoryRequest("memory_list", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "No memories stored")
	})

	t.Run("with entries", func(t *testing.T) {
		require.NoError(
			t,
			store.Save(
				domain.MemoryEntry{ID: "x", Category: "knowledge", Title: "X", Content: "c", Date: "2026-01-01"},
			),
		)
		req := makeMemoryRequest("memory_list", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		text := res.Content[0].(mcplib.TextContent).Text
		assert.Contains(t, text, "id: x")
		assert.Contains(t, text, "title: X")
	})
}
