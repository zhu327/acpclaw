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
