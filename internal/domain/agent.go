package domain

import (
	"context"
	"time"
)

// SessionInfo holds session metadata.
type SessionInfo struct {
	SessionID string
	Workspace string
	Title     string
	UpdatedAt time.Time
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

// SessionManager handles session lifecycle.
type SessionManager interface {
	NewSession(ctx context.Context, chat ChatRef, workspace string) error
	LoadSession(ctx context.Context, chat ChatRef, sessionID, workspace string) error
	ListSessions(ctx context.Context, chat ChatRef) ([]SessionInfo, error)
	ActiveSession(chat ChatRef) *SessionInfo
	Reconnect(ctx context.Context, chat ChatRef, workspace string) error
	Shutdown()
}

// Prompter handles agent prompt execution.
type Prompter interface {
	Prompt(ctx context.Context, chat ChatRef, input PromptInput) (*AgentReply, error)
	Cancel(ctx context.Context, chat ChatRef) error
}

// ModelInfo holds information about an available model.
type ModelInfo struct {
	ID          string
	Name        string
	Description string
}

// ModelState holds the current model state for a session.
type ModelState struct {
	CurrentModelID string
	Available      []ModelInfo
}

// ModelManager handles model listing and switching.
type ModelManager interface {
	GetModelState(chat ChatRef) (*ModelState, error)
	SetSessionModel(ctx context.Context, chat ChatRef, modelID string) error
}

// ModeInfo holds information about an available mode.
type ModeInfo struct {
	ID          string
	Name        string
	Description string
}

// ModeState holds the current mode state for a session.
type ModeState struct {
	CurrentModeID string
	Available     []ModeInfo
}

// ModeManager handles mode listing and switching.
type ModeManager interface {
	GetModeState(chat ChatRef) (*ModeState, error)
	SetSessionMode(ctx context.Context, chat ChatRef, modeID string) error
}

// PermissionHandler manages permission request wiring.
type PermissionHandler interface {
	SetPermissionHandler(fn func(chat ChatRef, req PermissionRequest) <-chan PermissionResponse)
	SetSessionPermissionMode(chat ChatRef, mode PermissionMode)
}

// ActivityObserver manages activity update wiring.
type ActivityObserver interface {
	SetActivityHandler(fn func(chat ChatRef, block ActivityBlock))
}

// Summarizer generates session summaries from conversation transcripts.
type Summarizer interface {
	Summarize(ctx context.Context, chat ChatRef, transcript string) (string, error)
}
