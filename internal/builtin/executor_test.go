package builtin

import (
	"context"
	"errors"
	"sync"
	"testing"

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
	mu       sync.Mutex
	reply    *domain.AgentReply
	err      error
	blockCh  chan struct{} // when non-nil, Prompt blocks until closed
	readyCh  chan struct{} // when non-nil, closed when Prompt is about to block
	captured domain.PromptInput
}

func (m *mockPrompter) Prompt(ctx context.Context, chat domain.ChatRef, input domain.PromptInput) (*domain.AgentReply, error) {
	m.mu.Lock()
	m.captured = input
	blockCh := m.blockCh
	readyCh := m.readyCh
	m.mu.Unlock()

	if blockCh != nil {
		if readyCh != nil {
			close(readyCh)
		}
		<-blockCh
	}

	m.mu.Lock()
	reply, err := m.reply, m.err
	m.mu.Unlock()
	return reply, err
}

func (m *mockPrompter) Cancel(ctx context.Context, chat domain.ChatRef) error {
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

func (m *mockPrompter) setBlocking(blockCh, readyCh chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockCh = blockCh
	m.readyCh = readyCh
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

func TestExecutePrompt_Success(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{}
	prompter.setReply(&domain.AgentReply{Text: "hello from agent"})

	exec := newPromptExecutor(sessionMgr, prompter, nil)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hi"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	result := exec.executePrompt(context.Background(), action, tc)

	require.NotNil(t, result)
	assert.False(t, result.SuppressOutbound)
	assert.Empty(t, result.Text)
	require.NotNil(t, result.Reply)
	assert.Equal(t, "hello from agent", result.Reply.Text)
}

func TestExecutePrompt_AgentError(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	prompter := &mockPrompter{}
	prompter.setError(errors.New("agent failed"))

	exec := newPromptExecutor(sessionMgr, prompter, nil)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hi"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	result := exec.executePrompt(context.Background(), action, tc)

	require.NotNil(t, result)
	assert.False(t, result.SuppressOutbound)
	assert.Equal(t, "❌ Failed to process your request.", result.Text)
	assert.Nil(t, result.Reply)
}

func TestExecutePrompt_Busy(t *testing.T) {
	sessionMgr := &mockSessionManager{activeSession: &domain.SessionInfo{}}
	blockCh := make(chan struct{})
	readyCh := make(chan struct{})
	prompter := &mockPrompter{}
	prompter.setReply(&domain.AgentReply{Text: "done"})
	prompter.setBlocking(blockCh, readyCh)

	exec := newPromptExecutor(sessionMgr, prompter, nil)

	action := domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: "hi"},
	}
	tc := makeTurnContext(testChat, action.Input, nil)

	var firstDone sync.WaitGroup
	firstDone.Add(1)
	go func() {
		defer firstDone.Done()
		exec.executePrompt(context.Background(), action, tc)
	}()

	// Wait for first goroutine to acquire lock and block in Prompt
	<-readyCh

	var secondResult *domain.Result
	var secondDone sync.WaitGroup
	secondDone.Add(1)
	go func() {
		defer secondDone.Done()
		secondResult = exec.executePrompt(context.Background(), action, tc)
	}()

	// Second call should return quickly with SuppressOutbound
	secondDone.Wait()
	require.NotNil(t, secondResult)
	assert.True(t, secondResult.SuppressOutbound, "second call while first is running should suppress outbound")

	close(blockCh)
	firstDone.Wait()
}

func TestExecutePrompt_FirstTurnPrefix(t *testing.T) {
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

	result := exec.executePrompt(context.Background(), action, tc)

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
	assert.Equal(t, "[Session Info]\nchannel: telegram\nchat_id: 12345\n[/Session Info]", block)

	chat2 := domain.ChatRef{ChannelKind: "test", ChatID: "1"}
	block2 := buildSessionInfoBlock(chat2)
	assert.Equal(t, "[Session Info]\nchannel: test\nchat_id: 1\n[/Session Info]", block2)
}
