package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/builtin/agent"
	"github.com/zhu327/acpclaw/internal/domain"
)

func chat(id string) domain.ChatRef {
	return domain.ChatRef{ChannelKind: "test", ChatID: id}
}

func TestEchoAgentService_NewSession(t *testing.T) {
	svc := agent.NewEchoAgentService()
	err := svc.NewSession(context.Background(), chat("100"), "/workspace")
	require.NoError(t, err)
	info := svc.ActiveSession(chat("100"))
	require.NotNil(t, info)
	assert.Equal(t, "/workspace", info.Workspace)
	assert.Contains(t, info.SessionID, "echo-test:100-")
}

func TestEchoAgentService_Prompt_Echo(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("1"), "/ws"))

	reply, err := svc.Prompt(context.Background(), chat("1"), domain.PromptInput{Text: "hello"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "hello")
}

func TestEchoAgentService_Prompt_NoSession(t *testing.T) {
	svc := agent.NewEchoAgentService()
	reply, err := svc.Prompt(context.Background(), chat("999"), domain.PromptInput{Text: "hi"})
	assert.ErrorIs(t, err, domain.ErrNoActiveSession)
	assert.Nil(t, reply)
}

// TestEchoAgentService_AskPermission_NoHandler verifies: when input contains [ask] but
// no handler is set, the reply notes the missing handler.
func TestEchoAgentService_AskPermission_NoHandler(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("2"), "/ws"))

	reply, err := svc.Prompt(context.Background(), chat("2"), domain.PromptInput{Text: "run [ask] please"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "run [ask] please")
	assert.Contains(t, reply.Text, "no handler set")
}

// TestEchoAgentService_AskPermission_ThisTime verifies: handler returns
// domain.PermissionThisTime and the decision is appended to the reply.
func TestEchoAgentService_AskPermission_ThisTime(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("3"), "/ws"))

	var capturedReq domain.PermissionRequest
	svc.SetPermissionHandler(func(ch domain.ChatRef, req domain.PermissionRequest) <-chan domain.PermissionResponse {
		capturedReq = req
		respCh := make(chan domain.PermissionResponse, 1)
		respCh <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return respCh
	})

	reply, err := svc.Prompt(context.Background(), chat("3"), domain.PromptInput{Text: "do [ask] now"})
	require.NoError(t, err)
	require.NotNil(t, reply)

	assert.Equal(t, "echo_tool", capturedReq.Tool)
	assert.NotEmpty(t, capturedReq.ID)
	assert.Contains(t, capturedReq.AvailableActions, domain.PermissionThisTime)
	assert.Contains(t, capturedReq.AvailableActions, domain.PermissionAlways)
	assert.Contains(t, capturedReq.AvailableActions, domain.PermissionDeny)
	assert.Equal(t, map[string]any{"text": "do [ask] now"}, capturedReq.Input)

	assert.Contains(t, reply.Text, "decision=this_time")
}

// TestEchoAgentService_AskPermission_Always verifies: handler returns domain.PermissionAlways.
func TestEchoAgentService_AskPermission_Always(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("4"), "/ws"))

	svc.SetPermissionHandler(func(_ domain.ChatRef, _ domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionAlways}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), chat("4"), domain.PromptInput{Text: "[ask]"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "decision=always")
}

// TestEchoAgentService_AskPermission_Deny verifies: handler returns domain.PermissionDeny.
func TestEchoAgentService_AskPermission_Deny(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("5"), "/ws"))

	svc.SetPermissionHandler(func(_ domain.ChatRef, _ domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), chat("5"), domain.PromptInput{Text: "[ask] sensitive"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "decision=deny")
}

// TestEchoAgentService_AskPermission_ChatRef verifies: handler receives the same ChatRef
// as the request.
func TestEchoAgentService_AskPermission_ChatRef(t *testing.T) {
	svc := agent.NewEchoAgentService()
	c := chat("42")
	require.NoError(t, svc.NewSession(context.Background(), c, "/ws"))

	var gotChat domain.ChatRef
	svc.SetPermissionHandler(func(ch domain.ChatRef, _ domain.PermissionRequest) <-chan domain.PermissionResponse {
		gotChat = ch
		respCh := make(chan domain.PermissionResponse, 1)
		respCh <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return respCh
	})

	_, err := svc.Prompt(context.Background(), c, domain.PromptInput{Text: "[ask]"})
	require.NoError(t, err)
	assert.Equal(t, c, gotChat)
}

// TestEchoAgentService_NoAsk_HandlerNotCalled verifies: when input lacks [ask],
// the handler is not invoked.
func TestEchoAgentService_NoAsk_HandlerNotCalled(t *testing.T) {
	svc := agent.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), chat("6"), "/ws"))

	called := false
	svc.SetPermissionHandler(func(_ domain.ChatRef, _ domain.PermissionRequest) <-chan domain.PermissionResponse {
		called = true
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), chat("6"), domain.PromptInput{Text: "just echo me"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.False(t, called, "handler should not be called when [ask] is absent")
	assert.NotContains(t, reply.Text, "permission asked")
}
