package memory_test

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/memory"
)

func TestHasSubstantiveContent(t *testing.T) {
	t.Run("template default content", func(t *testing.T) {
		// Body only (store.Get returns content after frontmatter)
		content := `## Background

(Name, background, location, etc.)

## Preferences

(Workflow, tools, habits)
`
		assert.False(t, memory.HasSubstantiveContent(content))
	})
	t.Run("has real content", func(t *testing.T) {
		content := "喜欢用 vim"
		assert.True(t, memory.HasSubstantiveContent(content))
	})
	t.Run("mixed template and real content", func(t *testing.T) {
		content := `---
title: "Preferences"
---

(Name, background)

喜欢用 vim 和 dark mode
`
		assert.True(t, memory.HasSubstantiveContent(content))
	})
	t.Run("empty string", func(t *testing.T) {
		assert.False(t, memory.HasSubstantiveContent(""))
	})
}

func TestService_BuildSessionContext(t *testing.T) {
	dir := t.TempDir()
	svc, err := memory.NewService(dir, dir, fstest.MapFS{})
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	t.Run("all slots template default", func(t *testing.T) {
		ctx := context.Background()
		out, err := svc.BuildSessionContext(ctx)
		require.NoError(t, err)
		assert.Empty(t, out)
	})

	t.Run("only owner-profile has content", func(t *testing.T) {
		require.NoError(t, svc.Save(domain.MemoryEntry{
			ID: "owner-profile", Category: "knowledge", Title: "Owner",
			Content: "John, engineer in SF.", Date: "2026-03-12",
		}))
		ctx := context.Background()
		out, err := svc.BuildSessionContext(ctx)
		require.NoError(t, err)
		assert.Contains(t, out, "[Memory Context")
		assert.Contains(t, out, "## Owner")
		assert.Contains(t, out, "John, engineer in SF.")
		assert.Contains(t, out, "[/Memory Context]")
	})

	t.Run("has episodes", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			require.NoError(t, svc.Save(domain.MemoryEntry{
				ID:       fmt.Sprintf("ep_%d", i),
				Category: "episode",
				Title:    fmt.Sprintf("Session %d", i),
				Content:  "summary",
				Date:     fmt.Sprintf("2026-03-%02d", 10+i),
			}))
		}
		ctx := context.Background()
		out, err := svc.BuildSessionContext(ctx)
		require.NoError(t, err)
		assert.Contains(t, out, "## Recent Sessions")
		// Last 3 episodes (List returns by date DESC, so newest first)
		assert.Contains(t, out, "2026-03-14")
		assert.Contains(t, out, "2026-03-13")
		assert.Contains(t, out, "2026-03-12")
	})

	t.Run("all sections present", func(t *testing.T) {
		svc2, _ := memory.NewService(t.TempDir(), t.TempDir(), fstest.MapFS{})
		defer func() { _ = svc2.Close() }()
		require.NoError(t, svc2.Save(domain.MemoryEntry{
			ID: "owner-profile", Category: "knowledge", Content: "Owner info", Date: "2026-03-12",
		}))
		require.NoError(t, svc2.Save(domain.MemoryEntry{
			ID: "preferences", Category: "knowledge", Content: "Prefs", Date: "2026-03-12",
		}))
		require.NoError(t, svc2.Save(domain.MemoryEntry{
			ID: "ep1", Category: "episode", Title: "S1", Content: "x", Date: "2026-03-11",
		}))
		ctx := context.Background()
		out, err := svc2.BuildSessionContext(ctx)
		require.NoError(t, err)
		assert.Contains(t, out, "## Owner")
		assert.Contains(t, out, "## Preferences")
		assert.Contains(t, out, "## Recent Sessions")
		assert.Contains(t, out, "[Memory Context")
		assert.Contains(t, out, "[/Memory Context]")
	})
}

func TestService_SaveAndRead(t *testing.T) {
	dir := t.TempDir()
	svc, err := memory.NewService(dir, dir, fstest.MapFS{})
	require.NoError(t, err)
	defer func() {
		_ = svc.Close()
	}()

	err = svc.Save(domain.MemoryEntry{
		ID:       "preferences",
		Category: "knowledge",
		Title:    "Preferences",
		Content:  "Prefers dark mode and vim keybindings.",
		Tags:     []string{"prefs"},
		Date:     "2026-03-12",
	})
	require.NoError(t, err)

	entry, err := svc.Read("preferences")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "preferences", entry.ID)
	assert.Contains(t, entry.Content, "dark mode")
}
