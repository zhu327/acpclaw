package acp

import (
	"time"

	"github.com/zhu327/acpclaw/internal/session"
	"github.com/zhu327/acpclaw/internal/util"
)

// ChatContext 存储当前会话的上下文信息
// Deprecated: 使用 session.Context 代替
type ChatContext struct {
	Channel   string    `json:"channel"`
	ChatID    string    `json:"chatId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

var (
	getChatContextDir = util.GetAcpclawContextDir
	defaultStore      = session.NewStore(util.GetAcpclawContextDir())
)

// WriteChatContext 原子写入最近活跃的 chat 上下文
// Deprecated: 使用 session.Store.Write 代替
func WriteChatContext(channel, chatID string) error {
	return defaultStore.Write(channel, chatID)
}

// ReadChatContext 读取最近的 chat 上下文
// Deprecated: 使用 session.Store.Read 代替
func ReadChatContext() (*ChatContext, error) {
	ctx, err := defaultStore.Read()
	if err != nil {
		return nil, err
	}
	return &ChatContext{
		Channel:   ctx.Channel,
		ChatID:    ctx.ChatID,
		UpdatedAt: ctx.UpdatedAt,
	}, nil
}
