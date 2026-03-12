package util

import "strings"

// LogTextPreview returns a collapsed, truncated preview of text for log output.
func LogTextPreview(text string, maxLen int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return "<empty>"
	}
	if len(collapsed) <= maxLen {
		return collapsed
	}
	return collapsed[:maxLen] + "..."
}
