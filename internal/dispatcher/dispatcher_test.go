package dispatcher_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/dispatcher"
	"github.com/zhu327/acpclaw/internal/domain"
)

// echoAgentStub is a minimal AgentService for dispatcher tests (avoids agent package import).
type echoAgentStub struct {
	mu       sync.RWMutex
	sessions map[string]domain.SessionInfo
}

func newEchoAgentStub() *echoAgentStub {
	return &echoAgentStub{sessions: make(map[string]domain.SessionInfo)}
}

func (e *echoAgentStub) NewSession(_ context.Context, chatID string, workspace string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions[chatID] = domain.SessionInfo{
		SessionID: fmt.Sprintf("echo-%s-%d", chatID, time.Now().UnixNano()),
		Workspace: workspace,
		UpdatedAt: time.Now(),
	}
	return nil
}

func (e *echoAgentStub) LoadSession(ctx context.Context, chatID string, _, workspace string) error {
	return e.NewSession(ctx, chatID, workspace)
}

func (e *echoAgentStub) ListSessions(_ context.Context, chatID string) ([]domain.SessionInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.sessions[chatID]; ok {
		return []domain.SessionInfo{s}, nil
	}
	return nil, domain.ErrNoActiveProcess
}

func (e *echoAgentStub) Prompt(_ context.Context, chatID string, input domain.PromptInput) (*domain.AgentReply, error) {
	e.mu.RLock()
	_, ok := e.sessions[chatID]
	e.mu.RUnlock()
	if !ok {
		return nil, domain.ErrNoActiveSession
	}
	return &domain.AgentReply{Text: input.Text}, nil
}
func (e *echoAgentStub) Cancel(_ context.Context, _ string) error { return nil }
func (e *echoAgentStub) Reconnect(ctx context.Context, chatID string, workspace string) error {
	return e.NewSession(ctx, chatID, workspace)
}

func (e *echoAgentStub) ActiveSession(chatID string) *domain.SessionInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.sessions[chatID]; ok {
		return &s
	}
	return nil
}
func (e *echoAgentStub) Shutdown()                                               {}
func (e *echoAgentStub) SetActivityHandler(_ func(string, domain.ActivityBlock)) {}

func (e *echoAgentStub) SetPermissionHandler(
	_ func(string, domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
}
func (e *echoAgentStub) SetSessionPermissionMode(_ string, _ domain.PermissionMode) {}

type mockResponder struct {
	mu         sync.Mutex
	replies    []domain.OutboundMessage
	activities []domain.ActivityBlock
}

func (r *mockResponder) Reply(msg domain.OutboundMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, msg)
	return nil
}
func (r *mockResponder) ShowPermissionUI(_ domain.ChannelPermissionRequest) error { return nil }
func (r *mockResponder) ShowTypingIndicator() error                               { return nil }
func (r *mockResponder) SendActivity(block domain.ActivityBlock) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activities = append(r.activities, block)
	return nil
}
func (r *mockResponder) ShowBusyNotification(_ string, _ int) (int, error) { return 1, nil }
func (r *mockResponder) ClearBusyNotification(_ int) error                 { return nil }
func (r *mockResponder) ShowResumeKeyboard(_ []domain.SessionChoice) error { return nil }

func TestDispatcher_Handle_SlashNew(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{DefaultWorkspace: "/tmp"})
	svc := newEchoAgentStub()
	d.SetAgentService(svc)

	resp := &mockResponder{}
	msg := domain.InboundMessage{ChatID: "100", Text: "/new", ChannelKind: "test"}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Session started")
}

func TestDispatcher_Handle_SlashStatus(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{})
	d.SetAgentService(newEchoAgentStub())

	resp := &mockResponder{}
	msg := domain.InboundMessage{ChatID: "100", Text: "/status", ChannelKind: "test"}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Status")
}

func TestDispatcher_Handle_SlashHelp(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{
		DefaultWorkspace: ".",
	})
	d.SetAgentService(nil) // no agent needed for /help

	resp := &mockResponder{}
	msg := domain.InboundMessage{
		ChatID:      "12345",
		Text:        "/help",
		ChannelKind: "test",
	}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "/new")
	assert.Contains(t, resp.replies[0].Text, "/help")
}
