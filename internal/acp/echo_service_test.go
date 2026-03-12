package acp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/acp"
)

func TestEchoAgentService_NewSession(t *testing.T) {
	svc := acp.NewEchoAgentService()
	err := svc.NewSession(context.Background(), 100, "/workspace")
	require.NoError(t, err)
	info := svc.ActiveSession(100)
	require.NotNil(t, info)
	assert.Equal(t, "/workspace", info.Workspace)
	assert.Contains(t, info.SessionID, "echo-100-")
}

func TestEchoAgentService_Prompt_Echo(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 1, "/ws"))

	reply, err := svc.Prompt(context.Background(), 1, acp.PromptInput{Text: "hello"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "hello")
}

func TestEchoAgentService_Prompt_NoSession(t *testing.T) {
	svc := acp.NewEchoAgentService()
	reply, err := svc.Prompt(context.Background(), 999, acp.PromptInput{Text: "hi"})
	assert.ErrorIs(t, err, acp.ErrNoActiveSession)
	assert.Nil(t, reply)
}

// TestEchoAgentService_AskPermission_NoHandler 验证：输入含 [ask] 但无 handler 时，回复注明无 handler
func TestEchoAgentService_AskPermission_NoHandler(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 2, "/ws"))

	reply, err := svc.Prompt(context.Background(), 2, acp.PromptInput{Text: "run [ask] please"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "run [ask] please")
	assert.Contains(t, reply.Text, "no handler set")
}

// TestEchoAgentService_AskPermission_ThisTime 验证：handler 返回 PermissionThisTime，决策被附在回复中
func TestEchoAgentService_AskPermission_ThisTime(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 3, "/ws"))

	var capturedReq acp.PermissionRequest
	svc.SetPermissionHandler(func(chatID int64, req acp.PermissionRequest) <-chan acp.PermissionResponse {
		capturedReq = req
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionThisTime}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), 3, acp.PromptInput{Text: "do [ask] now"})
	require.NoError(t, err)
	require.NotNil(t, reply)

	// 验证 permission request 字段
	assert.Equal(t, "echo_tool", capturedReq.Tool)
	assert.NotEmpty(t, capturedReq.ID)
	assert.Contains(t, capturedReq.AvailableActions, acp.PermissionThisTime)
	assert.Contains(t, capturedReq.AvailableActions, acp.PermissionAlways)
	assert.Contains(t, capturedReq.AvailableActions, acp.PermissionDeny)
	assert.Equal(t, map[string]any{"text": "do [ask] now"}, capturedReq.Input)

	// 验证回复中包含决策结果
	assert.Contains(t, reply.Text, "decision=this_time")
}

// TestEchoAgentService_AskPermission_Always 验证：handler 返回 PermissionAlways
func TestEchoAgentService_AskPermission_Always(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 4, "/ws"))

	svc.SetPermissionHandler(func(_ int64, _ acp.PermissionRequest) <-chan acp.PermissionResponse {
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionAlways}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), 4, acp.PromptInput{Text: "[ask]"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "decision=always")
}

// TestEchoAgentService_AskPermission_Deny 验证：handler 返回 PermissionDeny
func TestEchoAgentService_AskPermission_Deny(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 5, "/ws"))

	svc.SetPermissionHandler(func(_ int64, _ acp.PermissionRequest) <-chan acp.PermissionResponse {
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionDeny}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), 5, acp.PromptInput{Text: "[ask] sensitive"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.Contains(t, reply.Text, "decision=deny")
}

// TestEchoAgentService_AskPermission_ChatID 验证：handler 收到的 chatID 与请求 chatID 一致
func TestEchoAgentService_AskPermission_ChatID(t *testing.T) {
	svc := acp.NewEchoAgentService()
	const chatID int64 = 42
	require.NoError(t, svc.NewSession(context.Background(), chatID, "/ws"))

	var gotChatID int64
	svc.SetPermissionHandler(func(cid int64, _ acp.PermissionRequest) <-chan acp.PermissionResponse {
		gotChatID = cid
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionThisTime}
		return ch
	})

	_, err := svc.Prompt(context.Background(), chatID, acp.PromptInput{Text: "[ask]"})
	require.NoError(t, err)
	assert.Equal(t, chatID, gotChatID)
}

// TestEchoAgentService_NoAsk_HandlerNotCalled 验证：不含 [ask] 时 handler 不被触发
func TestEchoAgentService_NoAsk_HandlerNotCalled(t *testing.T) {
	svc := acp.NewEchoAgentService()
	require.NoError(t, svc.NewSession(context.Background(), 6, "/ws"))

	called := false
	svc.SetPermissionHandler(func(_ int64, _ acp.PermissionRequest) <-chan acp.PermissionResponse {
		called = true
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionThisTime}
		return ch
	})

	reply, err := svc.Prompt(context.Background(), 6, acp.PromptInput{Text: "just echo me"})
	require.NoError(t, err)
	require.NotNil(t, reply)
	assert.False(t, called, "handler should not be called when [ask] is absent")
	assert.NotContains(t, reply.Text, "permission asked")
}
