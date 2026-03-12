package memory_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/memory"
)

func TestHistory_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)

	err := h.Append("chat-123", "user", "Hello")
	require.NoError(t, err)
	err = h.Append("chat-123", "assistant", "Hi there!")
	require.NoError(t, err)

	content, err := h.ReadUnsummarized("chat-123")
	require.NoError(t, err)
	assert.Contains(t, content, "[user] Hello")
	assert.Contains(t, content, "[assistant] Hi there!")
}

func TestHistory_MarkSummarized(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)

	_ = h.Append("chat-456", "user", "First message")
	_ = h.Append("chat-456", "assistant", "First reply")

	err := h.MarkSummarized("chat-456")
	require.NoError(t, err)

	_ = h.Append("chat-456", "user", "Second message")

	content, err := h.ReadUnsummarized("chat-456")
	require.NoError(t, err)
	assert.NotContains(t, content, "First message")
	assert.Contains(t, content, "Second message")
}
