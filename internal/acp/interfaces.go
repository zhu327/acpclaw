package acp

import "context"

// AgentService is the interface between the Telegram bot and the ACP agent.
type AgentService interface {
	NewSession(ctx context.Context, chatID int64, workspace string) error
	LoadSession(ctx context.Context, chatID int64, sessionID, workspace string) error
	ListResumableSessions(ctx context.Context, chatID int64) ([]SessionInfo, error)
	Prompt(ctx context.Context, chatID int64, input PromptInput) (*AgentReply, error)
	Cancel(ctx context.Context, chatID int64) error
	Stop(ctx context.Context, chatID int64) error
	ActiveSession(chatID int64) *SessionInfo
	Shutdown()
	SetActivityHandler(fn func(chatID int64, block ActivityBlock))
	SetPermissionHandler(fn func(chatID int64, req PermissionRequest) <-chan PermissionResponse)
	SetSessionPermissionMode(chatID int64, mode PermissionMode)
}

var _ AgentService = (*AcpAgentService)(nil)
