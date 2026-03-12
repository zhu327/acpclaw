package acp

import "time"

// PermissionMode represents how the client handles permission requests.
type PermissionMode string

const (
	PermissionModeAsk     PermissionMode = "ask"
	PermissionModeApprove PermissionMode = "approve"
	PermissionModeDeny    PermissionMode = "deny"
)

// PermissionDecision represents the user's choice for a permission request.
type PermissionDecision string

const (
	PermissionAlways   PermissionDecision = "always"
	PermissionThisTime PermissionDecision = "this_time"
	PermissionDeny     PermissionDecision = "deny"
)

// PermissionRequest holds details of a permission request from the agent.
// AvailableActions lists which decisions the user can choose (Python parity: available_actions).
type PermissionRequest struct {
	ID               string
	Tool             string
	Description      string
	Input            map[string]any
	AvailableActions []PermissionDecision
}

// PermissionResponse holds the user's decision for a permission request.
type PermissionResponse struct {
	Decision PermissionDecision
}

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
	Kind    ActivityKind
	Label   string
	Detail  string
	Text    string // accumulated tool output text
	Status  string // "in_progress", "completed", or "failed"
	StartAt time.Time
	EndAt   time.Time
}

// ImageData holds inline image content.
type ImageData struct {
	MIMEType string
	Data     []byte
	Name     string
}

// FileData holds inline file content.
// TextContent is set when the file is UTF-8 decodable (text file semantic); nil for binary.
// Task 3 uses this for prompt block construction (File: <name>\n\n<content> vs Binary file attached).
type FileData struct {
	MIMEType    string
	Data        []byte
	Name        string
	TextContent *string // non-nil when UTF-8 decodable; Task 3 handoff
}

// PromptInput represents user input to the agent.
type PromptInput struct {
	Text   string
	Images []ImageData
	Files  []FileData
}

// AgentReply holds the agent's response to forward to the user.
type AgentReply struct {
	Text       string
	Images     []ImageData
	Files      []FileData
	Activities []ActivityBlock
}

// SessionInfo holds session metadata.
type SessionInfo struct {
	SessionID string
	Workspace string
	Title     string
	UpdatedAt time.Time
}
