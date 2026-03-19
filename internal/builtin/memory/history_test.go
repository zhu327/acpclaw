package memory_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/builtin/memory"
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

func TestHistory_ReadUnsummarizedWithSpans(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)

	err := h.Append("chat-span", "user", "Hello World")
	require.NoError(t, err)

	content, spans, err := h.ReadUnsummarizedWithSpans("chat-span")
	require.NoError(t, err)
	assert.Contains(t, content, "Hello World")
	require.Len(t, spans, 1)
	assert.Equal(t, int64(0), spans[0].Start)
	assert.Greater(t, spans[0].End, int64(0))
	assert.NotEmpty(t, spans[0].Date)
}

func TestHistory_ReadUnsummarizedWithSpans_SkipsSummarized(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)

	_ = h.Append("chat-skip", "user", "First message")
	err := h.MarkSummarized("chat-skip")
	require.NoError(t, err)
	_ = h.Append("chat-skip", "user", "Second message")

	content, spans, err := h.ReadUnsummarizedWithSpans("chat-skip")
	require.NoError(t, err)
	assert.NotContains(t, content, "First message")
	assert.Contains(t, content, "Second message")
	require.Len(t, spans, 1)
	// Span should start after "First message" content, not at 0
	assert.Greater(t, spans[0].Start, int64(0))
	assert.Greater(t, spans[0].End, spans[0].Start)
}

func TestHistory_ReadRawHistory(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)
	_ = h.Append("chat-raw", "user", "1234567890")

	date := time.Now().Format("2006-01-02")
	// Append writes "[user] 1234567890\n\n"
	// "[user] " is 7 bytes, "1234567890" is 10 bytes → range [7, 17)
	raw, err := h.ReadRawHistory("chat-raw", date, 7, 17)
	require.NoError(t, err)
	assert.Equal(t, "1234567890", raw)
}

func TestHistory_ReadRawHistory_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	h := memory.NewHistory(dir)

	// chatID escaping historyDir entirely
	_, err := h.ReadRawHistory("../outside", "2026-01-01", 0, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escape")

	// deep traversal escaping historyDir
	_, err = h.ReadRawHistory("../../outside", "2026-01-01", 0, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escape")
}
