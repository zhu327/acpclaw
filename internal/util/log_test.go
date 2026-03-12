package util

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogTextPreview(t *testing.T) {
	assert.Equal(t, "hello world", LogTextPreview("  hello   world  ", 200))
	assert.Equal(t, "<empty>", LogTextPreview("   ", 200))
	assert.Equal(t, "<empty>", LogTextPreview("", 200))

	long := strings.Repeat("y", 400)
	preview := LogTextPreview(long, 200)
	assert.True(t, strings.HasSuffix(preview, "..."))
	assert.LessOrEqual(t, len(preview), 203)
}
