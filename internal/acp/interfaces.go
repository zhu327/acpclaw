package acp

import "context"

// AgentService is the interface between the Telegram bot and the ACP agent.
type AgentService interface {
	// NewSession 在现有进程上调 new_session，无进程时先 spawn
	NewSession(ctx context.Context, chatID int64, workspace string) error
	// LoadSession 在现有进程上调 load_session，无进程时先 spawn
	LoadSession(ctx context.Context, chatID int64, sessionID, workspace string) error
	// ListSessions 在现有进程上调 session/list，返回所有 session（含当前活跃的）
	ListSessions(ctx context.Context, chatID int64) ([]SessionInfo, error)
	// Prompt 发送 prompt 到 agent
	Prompt(ctx context.Context, chatID int64, input PromptInput) (*AgentReply, error)
	// Cancel 取消当前 prompt
	Cancel(ctx context.Context, chatID int64) error
	// Reconnect 杀掉 ACP 进程并重新 spawn + new_session
	Reconnect(ctx context.Context, chatID int64, workspace string) error
	// ActiveSession 返回当前活跃 session 信息
	ActiveSession(chatID int64) *SessionInfo
	// Shutdown 停止所有进程
	Shutdown()
	// SetActivityHandler 设置 activity 回调
	SetActivityHandler(fn func(chatID int64, block ActivityBlock))
	// SetPermissionHandler 设置 permission 回调
	SetPermissionHandler(fn func(chatID int64, req PermissionRequest) <-chan PermissionResponse)
	// SetSessionPermissionMode 设置 session 的权限模式
	SetSessionPermissionMode(chatID int64, mode PermissionMode)
}

var _ AgentService = (*AcpAgentService)(nil)
