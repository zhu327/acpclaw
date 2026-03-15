package builtin

import "testing"

func TestExtractTitleFromSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{
			name:    "with frontmatter title",
			summary: "---\ntitle: \"Fix login bug\"\ndate: 2026-03-15\n---\n\n## Summary\nFixed the login bug.",
			want:    "Fix login bug",
		},
		{
			name:    "with unquoted title",
			summary: "---\ntitle: Fix login bug\ndate: 2026-03-15\n---\n\n## Summary",
			want:    "Fix login bug",
		},
		{
			name:    "no frontmatter",
			summary: "## Summary\nJust a summary without frontmatter.",
			want:    "Session summary",
		},
		{
			name:    "empty title",
			summary: "---\ntitle: \"\"\ndate: 2026-03-15\n---\n\n## Summary",
			want:    "Session summary",
		},
		{
			name:    "empty summary",
			summary: "",
			want:    "Session summary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTitleFromSummary(tt.summary)
			if got != tt.want {
				t.Errorf("extractTitleFromSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}
