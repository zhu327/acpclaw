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

// AgentService is the interface between the IM channel and the ACP agent.
type AgentService interface {
	// NewSession spawns or reuses a process and calls new_session.
	NewSession(ctx context.Context, chatID string, workspace string) error
	// LoadSession loads an existing session on the ACP process.
	LoadSession(ctx context.Context, chatID string, sessionID, workspace string) error
	// ListSessions returns all known sessions for the chat.
	ListSessions(ctx context.Context, chatID string) ([]SessionInfo, error)
	// Prompt sends user input to the agent and returns the reply.
	Prompt(ctx context.Context, chatID string, input PromptInput) (*AgentReply, error)
	// Cancel cancels the current in-flight prompt.
	Cancel(ctx context.Context, chatID string) error
	// Reconnect kills the ACP process and restarts with a new session.
	Reconnect(ctx context.Context, chatID string, workspace string) error
	// ActiveSession returns the current active session info, or nil.
	ActiveSession(chatID string) *SessionInfo
	// Shutdown stops all managed processes.
	Shutdown()
	// SetActivityHandler registers a callback for agent activity updates.
	SetActivityHandler(fn func(chatID string, block ActivityBlock))
	// SetPermissionHandler registers a callback for permission requests.
	SetPermissionHandler(fn func(chatID string, req PermissionRequest) <-chan PermissionResponse)
	// SetSessionPermissionMode sets the permission mode for a session.
	SetSessionPermissionMode(chatID string, mode PermissionMode)
}

// Summarizer generates a textual summary of a conversation transcript.
type Summarizer interface {
	Summarize(ctx context.Context, transcript string) (summary string, err error)
}
