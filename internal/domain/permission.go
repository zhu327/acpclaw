package domain

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
