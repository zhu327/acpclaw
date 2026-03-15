package telegram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"ASCII", "hello", 5},
		{"empty", "", 0},
		{"emoji surrogate pair", "hello 👋", 8},
		{"mixed", "a🀀b", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UTF16Len(tt.s)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderMarkdown_PlainText(t *testing.T) {
	chunks := RenderMarkdown("hello world")
	require.Len(t, chunks, 1)
	assert.Equal(t, "hello world", chunks[0].Text)
}

func TestRenderMarkdown_PlainTextSpecialChars(t *testing.T) {
	// Special chars in plain text must be escaped.
	chunks := RenderMarkdown("price: 1.5 (USD)")
	require.Len(t, chunks, 1)
	assert.Equal(t, `price: 1\.5 \(USD\)`, chunks[0].Text)
}

func TestRenderMarkdown_Bold(t *testing.T) {
	chunks := RenderMarkdown("hello **world**")
	require.Len(t, chunks, 1)
	// **bold** is converted to MarkdownV2 *bold* syntax.
	assert.Equal(t, `hello *world*`, chunks[0].Text)
}

func TestRenderMarkdown_BoldMDV2(t *testing.T) {
	// markdownToMDV2 wraps bold in single * (MarkdownV2 bold syntax).
	result := markdownToMDV2("hello **world**")
	assert.Equal(t, `hello *world*`, result)
}

func TestRenderMarkdown_ItalicMDV2(t *testing.T) {
	result := markdownToMDV2("hello *world*")
	assert.Equal(t, `hello _world_`, result)
}

func TestRenderMarkdown_InlineCode(t *testing.T) {
	result := markdownToMDV2("use `code`")
	assert.Equal(t, "use `code`", result)
}

func TestRenderMarkdown_InlineCodeEscapesBacktick(t *testing.T) {
	result := markdownToMDV2("use `co` + `de`")
	assert.Equal(t, "use `co` \\+ `de`", result)
}

func TestRenderMarkdown_FencedCodeBlock(t *testing.T) {
	result := markdownToMDV2("```go\nfmt.Println()\n```")
	assert.Equal(t, "```go\nfmt.Println()\n```", result)
}

func TestRenderMarkdown_Strikethrough(t *testing.T) {
	result := markdownToMDV2("hello ~~world~~")
	assert.Equal(t, `hello ~world~`, result)
}

func TestRenderMarkdown_Link(t *testing.T) {
	result := markdownToMDV2("[click](https://example.com)")
	assert.Equal(t, `[click](https://example.com)`, result)
}

func TestRenderMarkdown_LongTextSplits(t *testing.T) {
	longText := strings.Repeat("a", 5000)
	chunks := RenderMarkdown(longText)
	require.NotEmpty(t, chunks)
	for _, c := range chunks {
		assert.LessOrEqual(t, UTF16Len(c.Text), 4096, "chunk exceeds 4096 UTF-16 units")
	}
	assert.GreaterOrEqual(t, len(chunks), 2)
}

// Python parity: split_entities prefers newline boundaries; split at \n when possible.
func TestSplitChunks_PrefersNewlineBoundary(t *testing.T) {
	// 4090 chars + "\n\n" + 10 chars = 4102 total; must split at \n\n
	part1 := strings.Repeat("x", 4090)
	part2 := strings.Repeat("y", 10)
	text := part1 + "\n\n" + part2
	chunks := splitChunks(text)
	require.Len(t, chunks, 2, "must split into 2 chunks")
	assert.True(t, strings.HasSuffix(chunks[0].Text, "\n\n"), "first chunk should end at paragraph boundary")
	assert.True(t, strings.HasPrefix(chunks[1].Text, "y"), "second chunk should start with content after newline")
}

func TestRenderMarkdown_EmptyString(t *testing.T) {
	chunks := RenderMarkdown("")
	assert.Nil(t, chunks)
}

func TestRenderMarkdown_UnorderedList(t *testing.T) {
	input := "- apple\n- banana\n- cherry"
	chunks := RenderMarkdown(input)
	require.NotEmpty(t, chunks)
	// Hyphens in list context are escaped as plain text since we don't parse list syntax.
	assert.Contains(t, chunks[0].Text, "apple")
	assert.Contains(t, chunks[0].Text, "banana")
	assert.Contains(t, chunks[0].Text, "cherry")
}

func TestRenderMarkdown_OrderedList(t *testing.T) {
	input := "1. first\n2. second\n3. third"
	chunks := RenderMarkdown(input)
	require.NotEmpty(t, chunks)
	assert.Contains(t, chunks[0].Text, "first")
	assert.Contains(t, chunks[0].Text, "second")
	assert.Contains(t, chunks[0].Text, "third")
}
