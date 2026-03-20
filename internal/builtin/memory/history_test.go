package memory_test

import (
	"strings"
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

func TestInsertRawReferences(t *testing.T) {
	chatKey := "telegram:99"
	spans := []memory.HistorySpan{
		{Date: "2026-03-20", Start: 10, End: 42},
		{Date: "2026-03-21", Start: 0, End: 100},
	}

	t.Run("inserts into existing front matter", func(t *testing.T) {
		in := "---\ntitle: \"Summary\"\ndate: 2026-03-12\n---\n\nBody line.\n"
		out := memory.InsertRawReferences(in, chatKey, spans)
		assert.Contains(t, out, "raw_references:\n")
		assert.Contains(t, out, "  - chat_key="+chatKey+", date=2026-03-20, start_offset=10, end_offset=42\n")
		assert.Contains(t, out, "  - chat_key="+chatKey+", date=2026-03-21, start_offset=0, end_offset=100\n")
		assert.Contains(t, out, "---\n\nBody line.\n")
		rawAt := strings.Index(out, "raw_references:")
		closeAt := strings.Index(out, "\n---\n\nBody")
		require.GreaterOrEqual(t, rawAt, 0)
		require.Greater(t, closeAt, 0)
		assert.Less(t, rawAt, closeAt)
	})

	t.Run("no spans returns unchanged", func(t *testing.T) {
		in := "---\ntitle: x\n---\n\ny\n"
		assert.Equal(t, in, memory.InsertRawReferences(in, chatKey, nil))
		assert.Equal(t, in, memory.InsertRawReferences(in, chatKey, []memory.HistorySpan{}))
	})

	t.Run("no front matter returns unchanged", func(t *testing.T) {
		in := "plain markdown\n---\nnot a fence\n"
		assert.Equal(t, in, memory.InsertRawReferences(in, chatKey, spans))
	})

	t.Run("single opening fence returns unchanged", func(t *testing.T) {
		in := "---\ntitle: only open\n"
		assert.Equal(t, in, memory.InsertRawReferences(in, chatKey, spans))
	})

	t.Run("adds newline before raw_references when missing before closing delimiter", func(t *testing.T) {
		// After "---\n", content[3:] first matches "---" at "y---" so delimiter is glued; prefix ends with 'y'.
		in := "---\nx: y---\n\nAfter\n"
		out := memory.InsertRawReferences(in, chatKey, spans[:1])
		assert.Contains(t, out, "y\nraw_references:\n")
		assert.Contains(t, out, "  - chat_key="+chatKey+", date=2026-03-20, start_offset=10, end_offset=42\n")
		assert.Contains(t, out, "---\n\nAfter\n")
	})
}
