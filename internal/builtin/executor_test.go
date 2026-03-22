package builtin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

var testChat = domain.ChatRef{ChannelKind: "test", ChatID: "1"}

// mockSessionManager implements domain.SessionManager for tests.
type mockSessionManager struct {
	activeSession *domain.SessionInfo
}

func (m *mockSessionManager) NewSession(ctx context.Context, chat domain.ChatRef, workspace string) error {
	return nil
}

func (m *mockSessionManager) LoadSession(ctx context.Context, chat domain.ChatRef, sessionID, workspace string) error {
	return nil
}

func (m *mockSessionManager) ListSessions(ctx context.Context, chat domain.ChatRef) ([]domain.SessionInfo, error) {
	return nil, nil
}

func (m *mockSessionManager) ActiveSession(chat domain.ChatRef) *domain.SessionInfo {
	return m.activeSession
}

func (m *mockSessionManager) Reconnect(ctx context.Context, chat domain.ChatRef, workspace string) error {
	return nil
}

func (m *mockSessionManager) Shutdown() {}

// mockPrompter implements domain.Prompter for tests.
type mockPrompter struct {
	mu                sync.Mutex
	reply             *domain.AgentReply
	err               error
	captured          domain.PromptInput
	lastResp          domain.Responder
	blockUntilCtxDone bool
	cancelCalled      int
}

func (m *mockPrompter) Prompt(
	ctx context.Context,
	chat domain.ChatRef,
	input domain.PromptInput,
	resp domain.Responder,
) (*domain.AgentReply, error) {
	if m.blockUntilCtxDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m.mu.Lock()
	m.captured = input
	m.lastResp = resp
	reply, err := m.reply, m.err
	m.mu.Unlock()
	return reply, err
}

func (m *mockPrompter) Cancel(ctx context.Context, chat domain.ChatRef) error {
	m.mu.Lock()
	m.cancelCalled++
	m.mu.Unlock()
	return nil
}

func (m *mockPrompter) setReply(r *domain.AgentReply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reply = r
	m.err = nil
}

func (m *mockPrompter) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reply = nil
	m.err = err
}

func (m *mockPrompter) getCaptured() domain.PromptInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.captured
}

// mockResponder implements domain.Responder for tests.
type mockResponder struct{}

func (m *mockResponder) Reply(msg domain.OutboundMessage) error {
	return nil
}

func (m *mockResponder) ChannelKind() string {
	return "test"
}

func (m *mockResponder) ShowPermissionUI(req domain.ChannelPermissionRequest) error {
	return nil
}

func (m *mockResponder) ShowTypingIndicator() error {
	return nil
}

func (m *mockResponder) SendActivity(block domain.ActivityBlock) error {
	return nil
}

func (m *mockResponder) ShowBusyNotification(token string, replyToMsgID int) (int, error) {
	return 1, nil
}

func (m *mockResponder) ClearBusyNotification(notifyMsgID int) error {
	return nil
}

func (m *mockResponder) ShowResumeKeyboard(sessions []domain.SessionChoice) error {
	return nil
}

func makeTurnContext(chat domain.ChatRef, input domain.PromptInput, resp domain.Responder) *domain.TurnContext {
	if resp == nil {
		resp = &mockResponder{}
	}
	return &domain.TurnContext{
		Chat:      chat,
		SessionID: "",
		Message:   domain.InboundMessage{ChatRef: chat, ID: "", Text: input.Text},
		Responder: resp,
		State:     domain.State{},
	}
}

func TestRunPromptJob_Success(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{}
	prompter.setReply(&domain.AgentReply{Text: "hello from agent"})

	exec := newPromptExecutor(sessionMgr, prompter, nil)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hi"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	result := exec.runPromptJob(context.Background(), &promptJob{action: action, tc: tc})

	require.NotNil(t, result)
	assert.Empty(t, result.Text)
	require.NotNil(t, result.Reply)
	assert.Equal(t, "hello from agent", result.Reply.Text)
}

func TestRunPromptJob_Timeout(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{blockUntilCtxDone: true}
	exec := newPromptExecutor(sessionMgr, prompter, nil)
	action := domain.Action{Kind: domain.ActionPrompt, Input: domain.PromptInput{Text: "hi"}}
	tc := makeTurnContext(testChat, action.Input, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := exec.runPromptJob(ctx, &promptJob{action: action, tc: tc})

	require.NotNil(t, result)
	assert.Equal(t, "⏱ Request timed out.", result.Text)
	assert.Nil(t, result.Reply)
	prompter.mu.Lock()
	n := prompter.cancelCalled
	prompter.mu.Unlock()
	assert.GreaterOrEqual(t, n, 1)
}

func TestRunPromptJob_AgentError(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{}
	prompter.setError(errors.New("agent failed"))

	exec := newPromptExecutor(sessionMgr, prompter, nil)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hi"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	result := exec.runPromptJob(context.Background(), &promptJob{action: action, tc: tc})

	require.NotNil(t, result)
	assert.Equal(t, "❌ Failed to process your request.", result.Text)
	assert.Nil(t, result.Reply)
}

func TestRunPromptJob_PassesResponder(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{}
	prompter.setReply(&domain.AgentReply{Text: "ok"})

	exec := newPromptExecutor(sessionMgr, prompter, nil)
	r := &mockResponder{}
	action := domain.Action{Kind: domain.ActionPrompt, Input: domain.PromptInput{Text: "x"}}
	tc := makeTurnContext(testChat, action.Input, r)

	_ = exec.runPromptJob(context.Background(), &promptJob{action: action, tc: tc})
	assert.Equal(t, r, prompter.lastResp)
}

func TestRunPromptJob_FirstTurnPrefix(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: nil}
	prompter := &mockPrompter{}
	prompter.setReply(&domain.AgentReply{Text: "ok"})

	prefixCalled := false
	var prefixChat domain.ChatRef
	firstPromptPrefix := func(chat domain.ChatRef) string {
		prefixCalled = true
		prefixChat = chat
		return "[Session Info]\nchannel: test\nchat_id: 1\n[/Session Info]"
	}

	exec := newPromptExecutor(sessionMgr, prompter, firstPromptPrefix)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hello"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	result := exec.runPromptJob(context.Background(), &promptJob{action: action, tc: tc})

	require.NotNil(t, result)
	assert.True(t, prefixCalled, "firstPromptPrefix should be called when ActiveSession is nil")
	assert.Equal(t, testChat, prefixChat)
	captured := prompter.getCaptured()
	expectedPrefix := "[Session Info]\nchannel: test\nchat_id: 1\n[/Session Info]"
	assert.Contains(t, captured.Text, expectedPrefix)
	assert.Contains(t, captured.Text, "---")
	assert.Contains(t, captured.Text, "hello")
}

func TestBuildSessionInfoBlock(t *testing.T) {
	chat := domain.ChatRef{ChannelKind: "telegram", ChatID: "12345"}
	block := buildSessionInfoBlock(chat)
	assert.Equal(t, "<session_info>\nchannel: telegram\nchat_id: 12345\n</session_info>", block)

	chat2 := domain.ChatRef{ChannelKind: "test", ChatID: "1"}
	block2 := buildSessionInfoBlock(chat2)
	assert.Equal(t, "<session_info>\nchannel: test\nchat_id: 1\n</session_info>", block2)
}
