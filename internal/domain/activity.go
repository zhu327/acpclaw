package domain

import "time"

// ActivityKind categorizes tool execution for display.
type ActivityKind string

const (
	ActivityThink   ActivityKind = "think"
	ActivityExecute ActivityKind = "execute"
	ActivityRead    ActivityKind = "read"
	ActivityEdit    ActivityKind = "edit"
	ActivityWrite   ActivityKind = "write"
	ActivitySearch  ActivityKind = "search"
)

// ActivityBlock represents a single tool execution or thinking phase.
type ActivityBlock struct {
	Kind      ActivityKind
	Label     string
	Detail    string
	Text      string // accumulated tool output text
	Status    string // "in_progress", "completed", or "failed"
	Workspace string // active workspace path at the time of the activity
	StartAt   time.Time
	EndAt     time.Time
}

// MaxThinkTextRunes is the maximum number of runes shown from a think block's Text.
const MaxThinkTextRunes = 100

// FormatActivityText returns a plain-text representation of an ActivityBlock
// suitable for channels that do not support rich Markdown formatting.
// For think blocks the thinking text is appended (truncated to MaxThinkTextRunes).
// For other blocks the detail is appended when it differs from the label.
func (b ActivityBlock) FormatActivityText() string {
	if b.Label == "" {
		return ""
	}
	if b.Kind == ActivityThink {
		text := TruncateRunes(b.Text, MaxThinkTextRunes)
		if text == "" {
			return b.Label
		}
		return b.Label + "\n" + text
	}
	if b.Detail != "" && b.Detail != b.Label {
		return b.Label + ": " + b.Detail
	}
	return b.Label
}

// TruncateRunes truncates s to at most maxRunes Unicode code points, appending "…" if truncated.
func TruncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
