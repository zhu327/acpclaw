package weixin

import (
	"context"
	"errors"
	"log/slog"

	"github.com/zhu327/acpclaw/internal/domain"
	weixinbot "github.com/zhu327/weixin-bot"
)

type sharedResponderNoOps struct{}

func (sharedResponderNoOps) ShowPermissionUI(domain.ChannelPermissionRequest) error { return nil }
func (sharedResponderNoOps) ShowBusyNotification(string, int) (int, error)          { return 0, nil }
func (sharedResponderNoOps) ClearBusyNotification(int) error                        { return nil }
func (sharedResponderNoOps) ShowResumeKeyboard([]domain.SessionChoice) error        { return nil }

// WeixinResponder implements domain.Responder for WeChat.
// ctx 来自 SDK 的消息处理回调，responder 生命周期不应超过该 context。
type WeixinResponder struct {
	sharedResponderNoOps

	ctx context.Context
	bot *weixinbot.Bot
	msg *weixinbot.IncomingMessage
}

var _ domain.Responder = (*WeixinResponder)(nil)

// NewWeixinResponder creates a new WeixinResponder.
func NewWeixinResponder(ctx context.Context, bot *weixinbot.Bot, msg *weixinbot.IncomingMessage) *WeixinResponder {
	return &WeixinResponder{ctx: ctx, bot: bot, msg: msg}
}

// ChannelKind returns the channel kind.
func (r *WeixinResponder) ChannelKind() string { return "weixin" }

// Reply sends an outbound message to the user.
func (r *WeixinResponder) Reply(msg domain.OutboundMessage) error {
	if msg.Text == "" {
		return nil
	}
	return r.bot.Reply(r.ctx, r.msg, msg.Text)
}

// ShowTypingIndicator sends a typing indicator.
// Silently ignores ErrMissingContextToken — this occurs before the first message round-trip
// when the context_token cache is empty.
func (r *WeixinResponder) ShowTypingIndicator() error {
	err := r.bot.SendTyping(r.ctx, r.msg.UserID)
	if errors.Is(err, weixinbot.ErrMissingContextToken) {
		return nil
	}
	return err
}

// SendActivity sends an agent activity block as a plain text message.
// Uses bot.Send() instead of bot.Reply() to avoid the StopTyping side-effect
// that Reply() always triggers, which would create an extra HTTP round-trip per activity.
func (r *WeixinResponder) SendActivity(block domain.ActivityBlock) error {
	text := block.FormatActivityText()
	if text == "" {
		return nil
	}
	// bot.Send() requires a cached context_token, which is set when the original
	// incoming message was received. Safe to call after the first OnMessage dispatch.
	return r.bot.Send(r.ctx, r.msg.UserID, text)
}

// BackgroundResponder implements domain.Responder for background tasks (e.g. cron).
// 使用 bot.Send() 发送消息，依赖 SDK 内部缓存的 context_token。
type BackgroundResponder struct {
	sharedResponderNoOps

	ctx    context.Context
	bot    *weixinbot.Bot
	userID string
}

var _ domain.Responder = (*BackgroundResponder)(nil)

// NewBackgroundResponder creates a new BackgroundResponder for cron/background tasks.
func NewBackgroundResponder(ctx context.Context, bot *weixinbot.Bot, userID string) *BackgroundResponder {
	return &BackgroundResponder{ctx: ctx, bot: bot, userID: userID}
}

// IsBackgroundResponder reports whether r is a WeChat BackgroundResponder.
func IsBackgroundResponder(r domain.Responder) bool {
	_, ok := r.(*BackgroundResponder)
	return ok
}

// ChannelKind returns the channel kind.
func (r *BackgroundResponder) ChannelKind() string { return "weixin" }

// Reply sends an outbound message using cached context token.
func (r *BackgroundResponder) Reply(msg domain.OutboundMessage) error {
	if msg.Text == "" {
		return nil
	}
	err := r.bot.Send(r.ctx, r.userID, msg.Text)
	if err != nil {
		slog.Warn("weixin background reply failed", "user_id", r.userID, "error", err)
	}
	return err
}

func (r *BackgroundResponder) ShowTypingIndicator() error              { return nil }
func (r *BackgroundResponder) SendActivity(domain.ActivityBlock) error { return nil }
