package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

var _ domain.AgentService = (*EchoAgentService)(nil)

// EchoAgentService is a minimal AgentService implementation for development and testing.
type EchoAgentService struct {
	mu                sync.RWMutex
	sessions          map[int64]domain.SessionInfo
	sessionHistory    map[int64][]domain.SessionInfo
	activityHandler   func(chatID int64, block domain.ActivityBlock)
	permissionHandler func(chatID int64, req domain.PermissionRequest) <-chan domain.PermissionResponse
}

// NewEchoAgentService creates a new EchoAgentService.
func NewEchoAgentService() *EchoAgentService {
	return &EchoAgentService{
		sessions:       make(map[int64]domain.SessionInfo),
		sessionHistory: make(map[int64][]domain.SessionInfo),
	}
}

// NewSession creates a new session with a generated ID.
func (e *EchoAgentService) NewSession(_ context.Context, chatID int64, workspace string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sessionID := fmt.Sprintf("echo-%d-%d", chatID, time.Now().UnixNano())
	info := domain.SessionInfo{SessionID: sessionID, Workspace: workspace, UpdatedAt: time.Now()}
	e.sessions[chatID] = info
	e.sessionHistory[chatID] = upsertCappedSessionHistory(e.sessionHistory[chatID], info)
	return nil
}

// LoadSession delegates to NewSession (ignores sessionID, starts fresh).
func (e *EchoAgentService) LoadSession(ctx context.Context, chatID int64, _ string, workspace string) error {
	return e.NewSession(ctx, chatID, workspace)
}

// ListSessions returns the session history for the chat, or ErrNoActiveProcess if no active session.
func (e *EchoAgentService) ListSessions(_ context.Context, chatID int64) ([]domain.SessionInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.sessions[chatID]; !ok {
		return nil, ErrNoActiveProcess
	}
	history := e.sessionHistory[chatID]
	if len(history) == 0 {
		return nil, nil
	}
	result := make([]domain.SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// Prompt returns an domain.AgentReply echoing the input.
// When input contains "[ask]", trigger a permission request to simulate the ask flow:
//   - If permissionHandler is not set, reply directly and note the missing handler.
//   - If permissionHandler is set, wait for the response and append the decision to the reply.
func (e *EchoAgentService) Prompt(_ context.Context, chatID int64, input domain.PromptInput) (*domain.AgentReply, error) {
	e.mu.RLock()
	info, ok := e.sessions[chatID]
	handler := e.permissionHandler
	e.mu.RUnlock()
	if !ok {
		return nil, ErrNoActiveSession
	}

	shortID := info.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	text := fmt.Sprintf("[%s] %s", shortID, input.Text)
	if len(input.Images) > 0 || len(input.Files) > 0 {
		text += fmt.Sprintf(" [images=%d files=%d]", len(input.Images), len(input.Files))
	}

	// When input includes [ask], trigger a permission request.
	if strings.Contains(input.Text, "[ask]") {
		req := domain.PermissionRequest{
			ID:               fmt.Sprintf("echo-perm-%d-%d", chatID, time.Now().UnixNano()),
			Tool:             "echo_tool",
			Description:      "EchoAgentService 请求执行 echo_tool 权限",
			Input:            map[string]any{"text": input.Text},
			AvailableActions: []domain.PermissionDecision{domain.PermissionAlways, domain.PermissionThisTime, domain.PermissionDeny},
		}

		if handler == nil {
			text += " [permission asked: no handler set]"
		} else {
			ch := handler(chatID, req)
			resp := <-ch
			text += fmt.Sprintf(" [permission asked: decision=%s]", string(resp.Decision))
		}
	}

	return &domain.AgentReply{Text: text}, nil
}

// Cancel is a no-op.
func (e *EchoAgentService) Cancel(_ context.Context, _ int64) error { return nil }

// Reconnect clears the session and history, then creates a new one.
func (e *EchoAgentService) Reconnect(ctx context.Context, chatID int64, workspace string) error {
	e.mu.Lock()
	delete(e.sessions, chatID)
	delete(e.sessionHistory, chatID)
	e.mu.Unlock()
	return e.NewSession(ctx, chatID, workspace)
}

// ActiveSession returns the session info for the chat, or nil.
func (e *EchoAgentService) ActiveSession(chatID int64) *domain.SessionInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	info, ok := e.sessions[chatID]
	if !ok {
		return nil
	}
	return &info
}

// Shutdown clears all sessions and history.
func (e *EchoAgentService) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions = make(map[int64]domain.SessionInfo)
	e.sessionHistory = make(map[int64][]domain.SessionInfo)
}

// SetActivityHandler stores the handler.
func (e *EchoAgentService) SetActivityHandler(fn func(chatID int64, block domain.ActivityBlock)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activityHandler = fn
}

// SetPermissionHandler stores the handler.
func (e *EchoAgentService) SetPermissionHandler(
	fn func(chatID int64, req domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionHandler = fn
}

// SetSessionPermissionMode is a no-op.
func (e *EchoAgentService) SetSessionPermissionMode(_ int64, _ domain.PermissionMode) {}
