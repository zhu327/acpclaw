package telegram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zhu327/acpclaw/internal/domain"
)

func TestFormatActivityMessage_ThinkTruncation(t *testing.T) {
	longText := strings.Repeat("这是一段很长的思考内容", 50) // well over 100 chars
	block := domain.ActivityBlock{
		Kind:   domain.ActivityThink,
		Label:  "💡 Thinking",
		Status: "completed",
		Text:   longText,
	}
	result := formatActivityMessage(block)
	assert.Contains(t, result, "💡 Thinking")
	assert.Contains(t, result, "...")
	// The text portion should be truncated
	lines := strings.SplitN(result, "\n\n", 3)
	if len(lines) >= 2 {
		// Text part (after label) should be short
		textPart := lines[len(lines)-1]
		assert.LessOrEqual(t, len([]rune(textPart)), 210, "truncated text should be around 100 runes + suffix")
	}
}

func TestFormatActivityMessage_ShortThinkNotTruncated(t *testing.T) {
	block := domain.ActivityBlock{
		Kind:   domain.ActivityThink,
		Label:  "💡 Thinking",
		Status: "completed",
		Text:   "short thought",
	}
	result := formatActivityMessage(block)
	assert.Contains(t, result, "short thought")
	assert.NotContains(t, result, "...")
}

func TestFormatActivityLine_ToolDisplay(t *testing.T) {
	block := domain.ActivityBlock{
		Kind:   domain.ActivityEdit,
		Label:  "✏️ Editing",
		Detail: "Edit src/main.go",
		Text:   "lots of tool output that should not appear",
		Status: "completed",
	}
	line := formatActivityLine(block)
	assert.Contains(t, line, "✏️ Editing")
	assert.NotContains(t, line, "lots of tool output")
}
