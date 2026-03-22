package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

var (
	_ domain.SessionManager        = (*EchoAgentService)(nil)
	_ domain.Prompter              = (*EchoAgentService)(nil)
	_ domain.PromptResponderSource = (*EchoAgentService)(nil)
	_ domain.PermissionHandler     = (*EchoAgentService)(nil)
	_ domain.ActivityObserver      = (*EchoAgentService)(nil)
	_ domain.ModelManager          = (*EchoAgentService)(nil)
	_ domain.ModeManager           = (*EchoAgentService)(nil)
)

// EchoAgentService is a minimal agent implementation for development and testing.
type EchoAgentService struct {
	mu                sync.RWMutex
	sessions          map[string]domain.SessionInfo
	sessionHistory    map[string][]domain.SessionInfo
	activityHandler   func(chat domain.ChatRef, block domain.ActivityBlock)
	permissionHandler func(chat domain.ChatRef, req domain.PermissionRequest) <-chan domain.PermissionResponse
	promptRespMu      sync.Mutex
	promptChatKey     string
	promptResponder   domain.Responder
}

// NewEchoAgentService creates a new EchoAgentService.
func NewEchoAgentService() *EchoAgentService {
	return &EchoAgentService{
		sessions:       make(map[string]domain.SessionInfo),
		sessionHistory: make(map[string][]domain.SessionInfo),
	}
}

// NewSession creates a new session with a generated ID.
func (e *EchoAgentService) NewSession(_ context.Context, chat domain.ChatRef, workspace string) error {
	key := chat.CompositeKey()
	e.mu.Lock()
	defer e.mu.Unlock()
	sessionID := fmt.Sprintf("echo-%s-%d", key, time.Now().UnixNano())
	info := domain.SessionInfo{SessionID: sessionID, Workspace: workspace, UpdatedAt: time.Now()}
	e.sessions[key] = info
	e.sessionHistory[key] = upsertCappedSessionHistory(e.sessionHistory[key], info)
	return nil
}

// LoadSession delegates to NewSession (ignores sessionID, starts fresh).
func (e *EchoAgentService) LoadSession(ctx context.Context, chat domain.ChatRef, _ string, workspace string) error {
	return e.NewSession(ctx, chat, workspace)
}

// ListSessions returns the session history for the chat, or ErrNoActiveProcess if no active session.
func (e *EchoAgentService) ListSessions(_ context.Context, chat domain.ChatRef) ([]domain.SessionInfo, error) {
	key := chat.CompositeKey()
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.sessions[key]; !ok {
		return nil, domain.ErrNoActiveProcess
	}
	history := e.sessionHistory[key]
	if len(history) == 0 {
		return nil, nil
	}
	result := make([]domain.SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// ActivePromptResponder returns the Responder for the in-flight echo Prompt.
func (e *EchoAgentService) ActivePromptResponder(chat domain.ChatRef) domain.Responder {
	key := chat.CompositeKey()
	e.promptRespMu.Lock()
	defer e.promptRespMu.Unlock()
	if key != e.promptChatKey {
		return nil
	}
	return e.promptResponder
}

func (e *EchoAgentService) setPromptResponderState(key string, r domain.Responder) {
	e.promptRespMu.Lock()
	e.promptChatKey = key
	e.promptResponder = r
	e.promptRespMu.Unlock()
}

func (e *EchoAgentService) clearPromptResponderState() {
	e.promptRespMu.Lock()
	e.promptChatKey = ""
	e.promptResponder = nil
	e.promptRespMu.Unlock()
}

// Prompt returns an AgentReply echoing the input.
// When input contains "[ask]", trigger a permission request to simulate the ask flow.
func (e *EchoAgentService) Prompt(
	_ context.Context,
	chat domain.ChatRef,
	input domain.PromptInput,
	resp domain.Responder,
) (*domain.AgentReply, error) {
	key := chat.CompositeKey()
	e.setPromptResponderState(key, resp)
	defer e.clearPromptResponderState()

	e.mu.RLock()
	info, ok := e.sessions[key]
	handler := e.permissionHandler
	e.mu.RUnlock()
	if !ok {
		return nil, domain.ErrNoActiveSession
	}

	shortID := info.SessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	text := fmt.Sprintf("[%s] %s", shortID, input.Text)
	if len(input.Images) > 0 || len(input.Files) > 0 {
		text += fmt.Sprintf(" [images=%d files=%d]", len(input.Images), len(input.Files))
	}

	if strings.Contains(input.Text, "[ask]") {
		req := domain.PermissionRequest{
			ID:          fmt.Sprintf("echo-perm-%s-%d", key, time.Now().UnixNano()),
			Tool:        "echo_tool",
			Description: "EchoAgentService 请求执行 echo_tool 权限",
			Input:       map[string]any{"text": input.Text},
			AvailableActions: []domain.PermissionDecision{
				domain.PermissionAlways,
				domain.PermissionThisTime,
				domain.PermissionDeny,
			},
		}

		if handler == nil {
			text += " [permission asked: no handler set]"
		} else {
			ch := handler(chat, req)
			permResp := <-ch
			text += fmt.Sprintf(" [permission asked: decision=%s]", string(permResp.Decision))
		}
	}

	return &domain.AgentReply{Text: text}, nil
}

// Cancel is a no-op.
func (e *EchoAgentService) Cancel(_ context.Context, _ domain.ChatRef) error { return nil }

// Reconnect clears the session and history, then creates a new one.
func (e *EchoAgentService) Reconnect(ctx context.Context, chat domain.ChatRef, workspace string) error {
	key := chat.CompositeKey()
	e.mu.Lock()
	delete(e.sessions, key)
	delete(e.sessionHistory, key)
	e.mu.Unlock()
	return e.NewSession(ctx, chat, workspace)
}

// ActiveSession returns the session info for the chat, or nil.
func (e *EchoAgentService) ActiveSession(chat domain.ChatRef) *domain.SessionInfo {
	key := chat.CompositeKey()
	e.mu.RLock()
	defer e.mu.RUnlock()
	info, ok := e.sessions[key]
	if !ok {
		return nil
	}
	return &info
}

// Shutdown clears all sessions and history.
func (e *EchoAgentService) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions = make(map[string]domain.SessionInfo)
	e.sessionHistory = make(map[string][]domain.SessionInfo)
}

// SetActivityHandler stores the handler.
func (e *EchoAgentService) SetActivityHandler(fn func(chat domain.ChatRef, block domain.ActivityBlock)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activityHandler = fn
}

// SetPermissionHandler stores the handler.
func (e *EchoAgentService) SetPermissionHandler(
	fn func(chat domain.ChatRef, req domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionHandler = fn
}

// SetSessionPermissionMode is a no-op.
func (e *EchoAgentService) SetSessionPermissionMode(_ domain.ChatRef, _ domain.PermissionMode) {}

// GetModelState returns ErrModelsNotSupported for the echo service.
func (e *EchoAgentService) GetModelState(_ domain.ChatRef) (*domain.ModelState, error) {
	return nil, domain.ErrModelsNotSupported
}

// SetSessionModel returns ErrModelsNotSupported for the echo service.
func (e *EchoAgentService) SetSessionModel(_ context.Context, _ domain.ChatRef, _ string) error {
	return domain.ErrModelsNotSupported
}

// GetModeState returns ErrModesNotSupported for the echo service.
func (e *EchoAgentService) GetModeState(_ domain.ChatRef) (*domain.ModeState, error) {
	return nil, domain.ErrModesNotSupported
}

// SetSessionMode returns ErrModesNotSupported for the echo service.
func (e *EchoAgentService) SetSessionMode(_ context.Context, _ domain.ChatRef, _ string) error {
	return domain.ErrModesNotSupported
}
