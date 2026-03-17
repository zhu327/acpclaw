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

func (m *mockPrompter) Prompt(
	ctx context.Context,
	chat domain.ChatRef,
	input domain.PromptInput,
) (*domain.AgentReply, error) {
	return nil, nil
}

func (m *mockPrompter) Cancel(ctx context.Context, chat domain.ChatRef) error {
	return m.cancelErr
}

type mockModelManager struct {
	getModelStateOut *domain.ModelState
	getModelStateErr error
	setModelErr      error
	setModelCalled   string
}

func (m *mockModelManager) GetModelState(_ domain.ChatRef) (*domain.ModelState, error) {
	return m.getModelStateOut, m.getModelStateErr
}

func (m *mockModelManager) SetSessionModel(_ context.Context, _ domain.ChatRef, modelID string) error {
	m.setModelCalled = modelID
	return m.setModelErr
}

type mockModeManager struct {
	getModeStateOut *domain.ModeState
	getModeStateErr error
	setModeErr      error
	setModeCalled   string
}

func (m *mockModeManager) GetModeState(_ domain.ChatRef) (*domain.ModeState, error) {
	return m.getModeStateOut, m.getModeStateErr
}

func (m *mockModeManager) SetSessionMode(_ context.Context, _ domain.ChatRef, modeID string) error {
	m.setModeCalled = modeID
	return m.setModeErr
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

	result, err := helpCmd.Execute(context.Background(), nil, tc)
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
	sm := &mockSessionManager{
		activeSession: &domain.SessionInfo{SessionID: "s-rc", Workspace: "/rc"},
	}
	cmd := NewReconnectCommand(sm, "/default")
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "99"},
		State: domain.State{},
	}

	_, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
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
	cmd := NewReconnectCommand(sm, "/default")
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

func TestModelCommand_ListModels(t *testing.T) {
	mm := &mockModelManager{
		getModelStateOut: &domain.ModelState{
			CurrentModelID: "gpt-4",
			Available: []domain.ModelInfo{
				{ID: "gpt-4", Name: "GPT-4", Description: "Most capable"},
				{ID: "gpt-3.5", Name: "GPT-3.5"},
			},
		},
	}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Available Models")
	assert.Contains(t, result.Text, "1. GPT-4")
	assert.Contains(t, result.Text, "ID: `gpt-4`")
	assert.Contains(t, result.Text, "Most capable")
	assert.Contains(t, result.Text, "2. GPT-3.5")
	assert.Contains(t, result.Text, "▶")
	assert.Contains(t, result.Text, "/model <number>")
}

func TestModelCommand_ListModels_NoSession(t *testing.T) {
	mm := &mockModelManager{getModelStateErr: domain.ErrNoActiveSession}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "No active session")
}

func TestModelCommand_ListModels_NotSupported(t *testing.T) {
	mm := &mockModelManager{getModelStateErr: domain.ErrModelsNotSupported}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "does not support model switching")
}

func TestModelCommand_SwitchModel_Success(t *testing.T) {
	mm := &mockModelManager{}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"gpt-4"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Switched to model")
	assert.Contains(t, result.Text, "gpt-4")
	assert.Equal(t, "gpt-4", mm.setModelCalled)
}

func TestModelCommand_SwitchModel_NotFound(t *testing.T) {
	mm := &mockModelManager{setModelErr: domain.ErrModelNotFound}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"nonexistent"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "not found")
	assert.Contains(t, result.Text, "/model")
}

func TestModelCommand_SwitchModel_NoSession(t *testing.T) {
	mm := &mockModelManager{setModelErr: domain.ErrNoActiveSession}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"gpt-4"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "No active session")
}

func TestModelCommand_SwitchModel_ByNumber(t *testing.T) {
	mm := &mockModelManager{
		getModelStateOut: &domain.ModelState{
			CurrentModelID: "gpt-4",
			Available: []domain.ModelInfo{
				{ID: "gpt-4", Name: "GPT-4"},
				{ID: "claude-sonnet", Name: "Sonnet"},
			},
		},
	}
	cmd := NewModelCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"2"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Switched to model")
	assert.Contains(t, result.Text, "claude-sonnet")
	assert.Equal(t, "claude-sonnet", mm.setModelCalled)
}

func TestModeCommand_ListModes(t *testing.T) {
	mm := &mockModeManager{
		getModeStateOut: &domain.ModeState{
			CurrentModeID: "agent",
			Available: []domain.ModeInfo{
				{ID: "agent", Name: "Agent", Description: "Full autonomous agent"},
				{ID: "ask", Name: "Ask", Description: "Read-only mode"},
			},
		},
	}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Text, "Available Modes")
	assert.Contains(t, result.Text, "1. Agent")
	assert.Contains(t, result.Text, "ID: `agent`")
	assert.Contains(t, result.Text, "Full autonomous agent")
	assert.Contains(t, result.Text, "2. Ask")
	assert.Contains(t, result.Text, "▶")
	assert.Contains(t, result.Text, "/mode <number>")
}

func TestModeCommand_ListModes_NoSession(t *testing.T) {
	mm := &mockModeManager{getModeStateErr: domain.ErrNoActiveSession}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "No active session")
}

func TestModeCommand_ListModes_NotSupported(t *testing.T) {
	mm := &mockModeManager{getModeStateErr: domain.ErrModesNotSupported}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), nil, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "does not support mode switching")
}

func TestModeCommand_SwitchMode_Success(t *testing.T) {
	mm := &mockModeManager{}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"ask"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Switched to mode")
	assert.Contains(t, result.Text, "ask")
	assert.Equal(t, "ask", mm.setModeCalled)
}

func TestModeCommand_SwitchMode_NotFound(t *testing.T) {
	mm := &mockModeManager{setModeErr: domain.ErrModeNotFound}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"nonexistent"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "not found")
	assert.Contains(t, result.Text, "/mode")
}

func TestModeCommand_SwitchMode_NoSession(t *testing.T) {
	mm := &mockModeManager{setModeErr: domain.ErrNoActiveSession}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"ask"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "No active session")
}

func TestModeCommand_SwitchMode_ByNumber(t *testing.T) {
	mm := &mockModeManager{
		getModeStateOut: &domain.ModeState{
			CurrentModeID: "agent",
			Available: []domain.ModeInfo{
				{ID: "agent", Name: "Agent"},
				{ID: "ask", Name: "Ask"},
			},
		},
	}
	cmd := NewModeCommand(mm)
	tc := &domain.TurnContext{
		Chat:  domain.ChatRef{ChannelKind: "test", ChatID: "1"},
		State: domain.State{},
	}

	result, err := cmd.Execute(context.Background(), []string{"2"}, tc)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Switched to mode")
	assert.Contains(t, result.Text, "ask")
	assert.Equal(t, "ask", mm.setModeCalled)
}
