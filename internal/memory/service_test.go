package memory_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/memory"
)

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
