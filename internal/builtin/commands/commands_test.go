package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

// --- Mocks ---

type mockSessionManager struct {
	newSessionErr    error
	reconnectErr     error
	listSessionsErr  error
	loadSessionErr   error
	activeSession    *domain.SessionInfo
	listSessionsOut  []domain.SessionInfo
	newSessionCalled bool
	reconnectCalled  bool
}

func (m *mockSessionManager) NewSession(ctx context.Context, chat domain.ChatRef, workspace string) error {
	m.newSessionCalled = true
	return m.newSessionErr
}

func (m *mockSessionManager) LoadSession(ctx context.Context, chat domain.ChatRef, sessionID, workspace string) error {
	return m.loadSessionErr
}

func (m *mockSessionManager) ListSessions(ctx context.Context, chat domain.ChatRef) ([]domain.SessionInfo, error) {
	if m.listSessionsErr != nil {
		return nil, m.listSessionsErr
	}
	return m.listSessionsOut, nil
}

func (m *mockSessionManager) ActiveSession(chat domain.ChatRef) *domain.SessionInfo {
	return m.activeSession
}

func (m *mockSessionManager) Reconnect(ctx context.Context, chat domain.ChatRef, workspace string) error {
	m.reconnectCalled = true
	return m.reconnectErr
}

func (m *mockSessionManager) Shutdown() {}

type mockPrompter struct {
	cancelErr error
}

func (m *mockPrompter) Prompt(ctx context.Context, chat domain.ChatRef, input domain.PromptInput) (*domain.AgentReply, error) {
	return nil, nil
}

func (m *mockPrompter) Cancel(ctx context.Context, chat domain.ChatRef) error {
	return m.cancelErr
}

// --- Tests ---

func TestNewCommand_Success(t *testing.T) {
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{
			SessionID: "sess-123",
			Workspace: "/home/user",
			Title:     "My Session",
			UpdatedAt: time.Now(),
		},
	}
	cmd := NewNewCommand(sm, "/default", nil)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, sm.newSessionCalled)
	assert.Contains(t, result.Text, "sess-123")
	assert.Contains(t, result.Text, "/home/user")
}

func TestNewCommand_Failure(t *testing.T) {
	sm := &mockSessionManager{
		newSessionErr: errors.New("session creation failed"),
	}
	cmd := NewNewCommand(sm, "/default", nil)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Failed to start session")
}

func TestCancelCommand_Success(t *testing.T) {
	p := &mockPrompter{}
	cmd := NewCancelCommand(p)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Cancelled")
}

func TestCancelCommand_NoSession(t *testing.T) {
	p := &mockPrompter{cancelErr: domain.ErrNoActiveSession}
	cmd := NewCancelCommand(p)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "No active session")
	assert.Contains(t, result.Text, "/new")
}

func TestStatusCommand_WithSession(t *testing.T) {
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{
			SessionID: "sess-456",
			Workspace: "/workspace",
			Title:     "Active",
			UpdatedAt: time.Now(),
		},
	}
	cmd := NewStatusCommand(sm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Status")
	assert.Contains(t, result.Text, "sess-456")
	assert.Contains(t, result.Text, "/workspace")
}

func TestStatusCommand_NoSession(t *testing.T) {
	sm := &mockSessionManager{activeSession: nil}
	cmd := NewStatusCommand(sm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Status")
	assert.Contains(t, result.Text, "No active session")
}

func TestStartCommand(t *testing.T) {
	cmd := NewStartCommand()
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Welcome")
	assert.Contains(t, result.Text, "/help")
}

func TestHelpCommand_WithCommands(t *testing.T) {
	cmd := NewHelpCommand()
	helpCmd := NewHelpCommand()
	startCmd := NewStartCommand()
	tc := &domain.TurnContext{
		Chat: domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{
			"commands": map[string]domain.Command{
				"help":  helpCmd,
				"start": startCmd,
			},
		},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "ACP-Claw Bot")
	assert.Contains(t, result.Text, "/help")
	assert.Contains(t, result.Text, "/start")
	assert.Contains(t, result.Text, "Show help")
	assert.Contains(t, result.Text, "Welcome message")
}

func TestSessionCommand_ListsSessions(t *testing.T) {
	sm := &mockSessionManager{
		listSessionsOut: []domain.SessionInfo{
			{SessionID: "s1", Workspace: "/w1", Title: "First", UpdatedAt: time.Now()},
			{SessionID: "s2", Workspace: "/w2", Title: "Second", UpdatedAt: time.Now()},
		},
		activeSession: &domain.SessionInfo{SessionID: "s1", Workspace: "/w1", Title: "First", UpdatedAt: time.Now()},
	}
	cmd := NewSessionCommand(sm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "First")
	assert.Contains(t, result.Text, "Second")
	assert.Contains(t, result.Text, "s1")
	assert.Contains(t, result.Text, "s2")
	assert.Contains(t, result.Text, "(active)")
}

func TestNewCommand_CallsBeforeSwitch(t *testing.T) {
	var calledChat domain.ChatRef
	beforeSwitch := func(ctx context.Context, chat domain.ChatRef) {
		calledChat = chat
	}
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{SessionID: "s1", Workspace: "/ws"},
	}
	cmd := NewNewCommand(sm, "/default", beforeSwitch)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "42"},
		State: domain.State{},
	}

	_, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Equal(t, "42", calledChat.ChatID)
	assert.True(t, sm.newSessionCalled, "NewSession should still be called after beforeSwitch")
}

func TestNewCommand_NilBeforeSwitch(t *testing.T) {
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{SessionID: "s1", Workspace: "/ws"},
	}
	cmd := NewNewCommand(sm, "/default", nil)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, sm.newSessionCalled)
}

func TestReconnectCommand_CallsBeforeSwitch(t *testing.T) {
	var calledChat domain.ChatRef
	beforeSwitch := func(ctx context.Context, chat domain.ChatRef) {
		calledChat = chat
	}
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{SessionID: "s-rc", Workspace: "/rc"},
	}
	cmd := NewReconnectCommand(sm, "/default", beforeSwitch)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "99"},
		State: domain.State{},
	}

	_, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Equal(t, "99", calledChat.ChatID)
	assert.True(t, sm.reconnectCalled)
}

func TestReconnectCommand_Success(t *testing.T) {
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{
			SessionID: "sess-reconnect",
			Workspace: "/reconnected",
			Title:     "Reconnected",
			UpdatedAt: time.Now(),
		},
	}
	cmd := NewReconnectCommand(sm, "/default", nil)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, sm.reconnectCalled)
	assert.Contains(t, result.Text, "reconnected")
	assert.Contains(t, result.Text, "sess-reconnect")
	assert.Contains(t, result.Text, "/reconnected")
}
