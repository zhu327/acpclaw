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

// PermissionHandler manages permission request wiring.
type PermissionHandler interface {
	SetPermissionHandler(fn func(chat ChatRef, req PermissionRequest) <-chan PermissionResponse)
	SetSessionPermissionMode(chat ChatRef, mode PermissionMode)
}

// ActivityObserver manages activity update wiring.
type ActivityObserver interface {
	SetActivityHandler(fn func(chat ChatRef, block ActivityBlock))
}

// Summarizer generates a textual summary of a conversation transcript.
type Summarizer interface {
	Summarize(ctx context.Context, transcript string) (summary string, err error)
}
