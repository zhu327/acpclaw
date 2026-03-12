package acp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var _ AgentService = (*EchoAgentService)(nil)

// EchoAgentService is a minimal AgentService implementation for development and testing.
// It echoes user input back with a session prefix and media hints.
type EchoAgentService struct {
	mu                sync.RWMutex
	sessions          map[int64]SessionInfo
	activityHandler   func(chatID int64, block ActivityBlock)
	permissionHandler func(chatID int64, req PermissionRequest) <-chan PermissionResponse
}

// NewEchoAgentService creates a new EchoAgentService.
func NewEchoAgentService() *EchoAgentService {
	return &EchoAgentService{
		sessions: make(map[int64]SessionInfo),
	}
}

// NewSession creates a new session with a generated ID.
func (e *EchoAgentService) NewSession(ctx context.Context, chatID int64, workspace string) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	sessionID := fmt.Sprintf("echo-%d-%d", chatID, time.Now().UnixNano())
	e.sessions[chatID] = SessionInfo{SessionID: sessionID, Workspace: workspace}
	return nil
}

// LoadSession delegates to NewSession (ignores sessionID, starts fresh).
func (e *EchoAgentService) LoadSession(ctx context.Context, chatID int64, sessionID, workspace string) error {
	_ = sessionID
	return e.NewSession(ctx, chatID, workspace)
}

// ListResumableSessions returns the current session as a single-item list, or nil if no session.
func (e *EchoAgentService) ListResumableSessions(ctx context.Context, chatID int64) ([]SessionInfo, error) {
	_ = ctx
	e.mu.RLock()
	info, ok := e.sessions[chatID]
	e.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return []SessionInfo{info}, nil
}

// Prompt returns an AgentReply echoing the input with session prefix and media hints.
func (e *EchoAgentService) Prompt(ctx context.Context, chatID int64, input PromptInput) (*AgentReply, error) {
	_ = ctx
	e.mu.RLock()
	info, ok := e.sessions[chatID]
	e.mu.RUnlock()
	if !ok {
		return nil, nil
	}

	shortID := info.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	text := fmt.Sprintf("[%s] %s", shortID, input.Text)
	if len(input.Images) > 0 || len(input.Files) > 0 {
		text += fmt.Sprintf(" [images=%d files=%d]", len(input.Images), len(input.Files))
	}

	return &AgentReply{Text: text}, nil
}

// Cancel returns nil (no-op).
func (e *EchoAgentService) Cancel(ctx context.Context, chatID int64) error {
	_ = ctx
	_ = chatID
	return nil
}

// Stop removes the session from the map. Returns ErrNoActiveSession if no session exists,
// matching the behaviour of AcpAgentService.Stop.
func (e *EchoAgentService) Stop(ctx context.Context, chatID int64) error {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sessions[chatID]; !ok {
		return ErrNoActiveSession
	}
	delete(e.sessions, chatID)
	return nil
}

// ActiveSession returns the session info for the chat, or nil if none.
func (e *EchoAgentService) ActiveSession(chatID int64) *SessionInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	info, ok := e.sessions[chatID]
	if !ok {
		return nil
	}
	return &info
}

// AllSessions returns a snapshot of all sessions.
func (e *EchoAgentService) AllSessions() map[int64]SessionInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[int64]SessionInfo, len(e.sessions))
	for k, v := range e.sessions {
		result[k] = v
	}
	return result
}

// Shutdown clears all sessions.
func (e *EchoAgentService) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions = make(map[int64]SessionInfo)
}

// SetActivityHandler stores the handler but does not use it.
func (e *EchoAgentService) SetActivityHandler(fn func(chatID int64, block ActivityBlock)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activityHandler = fn
}

// SetPermissionHandler stores the handler but does not use it.
func (e *EchoAgentService) SetPermissionHandler(fn func(chatID int64, req PermissionRequest) <-chan PermissionResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionHandler = fn
}

// SetSessionPermissionMode is a no-op for the echo service.
func (e *EchoAgentService) SetSessionPermissionMode(chatID int64, mode PermissionMode) {
	_ = chatID
	_ = mode
}
