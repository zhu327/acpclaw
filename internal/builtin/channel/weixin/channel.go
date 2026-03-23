package weixin

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
	weixinbot "github.com/zhu327/weixin-bot"
)

// WeixinChannel implements domain.Channel for the WeChat platform.
type WeixinChannel struct {
	bot *weixinbot.Bot
}

var _ domain.Channel = (*WeixinChannel)(nil)

// NewWeixinChannel creates a new WeixinChannel.
func NewWeixinChannel(bot *weixinbot.Bot) *WeixinChannel {
	return &WeixinChannel{bot: bot}
}

// Kind returns the channel kind identifier.
func (c *WeixinChannel) Kind() string { return "weixin" }

// Start registers the message handler and begins the long-poll loop.
// Blocks until ctx is cancelled, matching the contract of other channel adapters.
func (c *WeixinChannel) Start(ctx context.Context, handler domain.MessageHandler) error {
	c.bot.OnMessage(func(msgCtx context.Context, msg *weixinbot.IncomingMessage) error {
		inbound := convertInbound(msg)
		if inbound.Text == "" {
			return nil
		}
		resp := NewWeixinResponder(msgCtx, c.bot, msg)
		_ = resp.ShowTypingIndicator()
		handler(msgCtx, inbound, resp)
		return nil
	})

	err := c.bot.Run(ctx)
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Stop is a no-op — shutdown is driven by context cancellation passed to Start.
func (c *WeixinChannel) Stop() error { return nil }

func convertInbound(msg *weixinbot.IncomingMessage) domain.InboundMessage {
	userID := msg.UserID
	text := strings.TrimSpace(msg.Text)
	return domain.InboundMessage{
		ChatRef:    domain.ChatRef{ChannelKind: "weixin", ChatID: userID},
		ID:         strconv.FormatInt(msg.Raw.MessageID, 10),
		Text:       text,
		AuthorID:   userID,
		AuthorName: userID,
	}
}
