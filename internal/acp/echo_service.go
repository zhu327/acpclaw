package acp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

var _ AgentService = (*EchoAgentService)(nil)

// EchoAgentService is a minimal AgentService implementation for development and testing.
type EchoAgentService struct {
	mu                sync.RWMutex
	sessions          map[int64]SessionInfo
	sessionHistory    map[int64][]SessionInfo
	activityHandler   func(chatID int64, block ActivityBlock)
	permissionHandler func(chatID int64, req PermissionRequest) <-chan PermissionResponse
}

// NewEchoAgentService creates a new EchoAgentService.
func NewEchoAgentService() *EchoAgentService {
	return &EchoAgentService{
		sessions:       make(map[int64]SessionInfo),
		sessionHistory: make(map[int64][]SessionInfo),
	}
}

// NewSession creates a new session with a generated ID.
func (e *EchoAgentService) NewSession(_ context.Context, chatID int64, workspace string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sessionID := fmt.Sprintf("echo-%d-%d", chatID, time.Now().UnixNano())
	info := SessionInfo{SessionID: sessionID, Workspace: workspace, UpdatedAt: time.Now()}
	e.sessions[chatID] = info
	e.sessionHistory[chatID] = append(e.sessionHistory[chatID], info)
	return nil
}

// LoadSession delegates to NewSession (ignores sessionID, starts fresh).
func (e *EchoAgentService) LoadSession(ctx context.Context, chatID int64, _ string, workspace string) error {
	return e.NewSession(ctx, chatID, workspace)
}

// ListSessions returns the session history for the chat, or ErrNoActiveProcess if no active session.
func (e *EchoAgentService) ListSessions(_ context.Context, chatID int64) ([]SessionInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.sessions[chatID]; !ok {
		return nil, ErrNoActiveProcess
	}
	history := e.sessionHistory[chatID]
	if len(history) == 0 {
		return nil, nil
	}
	result := make([]SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// Prompt returns an AgentReply echoing the input.
// 当输入文本包含 "[ask]" 时，触发一次 permission request 模拟 ask 场景：
//   - 若 permissionHandler 未设置，则直接回复并注明无 handler
//   - 若 permissionHandler 已设置，则等待其响应后将决策结果附在回复中
func (e *EchoAgentService) Prompt(_ context.Context, chatID int64, input PromptInput) (*AgentReply, error) {
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

	// 当输入包含 [ask] 时，触发一次 permission request
	if strings.Contains(input.Text, "[ask]") {
		req := PermissionRequest{
			ID:               fmt.Sprintf("echo-perm-%d-%d", chatID, time.Now().UnixNano()),
			Tool:             "echo_tool",
			Description:      "EchoAgentService 请求执行 echo_tool 权限",
			Input:            map[string]any{"text": input.Text},
			AvailableActions: []PermissionDecision{PermissionAlways, PermissionThisTime, PermissionDeny},
		}

		if handler == nil {
			text += " [permission asked: no handler set]"
		} else {
			ch := handler(chatID, req)
			resp := <-ch
			text += fmt.Sprintf(" [permission asked: decision=%s]", string(resp.Decision))
		}
	}

	return &AgentReply{Text: text}, nil
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
func (e *EchoAgentService) ActiveSession(chatID int64) *SessionInfo {
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
	e.sessions = make(map[int64]SessionInfo)
	e.sessionHistory = make(map[int64][]SessionInfo)
}

// SetActivityHandler stores the handler.
func (e *EchoAgentService) SetActivityHandler(fn func(chatID int64, block ActivityBlock)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activityHandler = fn
}

// SetPermissionHandler stores the handler.
func (e *EchoAgentService) SetPermissionHandler(fn func(chatID int64, req PermissionRequest) <-chan PermissionResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionHandler = fn
}

// SetSessionPermissionMode is a no-op.
func (e *EchoAgentService) SetSessionPermissionMode(_ int64, _ PermissionMode) {}
