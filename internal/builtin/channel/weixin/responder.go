package weixin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/zhu327/acpclaw/internal/domain"
	weixinbot "github.com/zhu327/weixin-bot"
)

type sharedResponderNoOps struct{}

func (sharedResponderNoOps) ShowPermissionUI(domain.ChannelPermissionRequest) error { return nil }
func (sharedResponderNoOps) ShowBusyNotification(string, int) (int, error)          { return 0, nil }
func (sharedResponderNoOps) ClearBusyNotification(int) error                        { return nil }
func (sharedResponderNoOps) ShowResumeKeyboard([]domain.SessionChoice) error        { return nil }

// activityAccumulatorLimit 限制活动累加器文本长度（rune 数），为 MaxMessageTextRunes 留余量。
const activityAccumulatorLimit = 1800

// WeixinResponder implements domain.Responder for WeChat.
// ctx 来自 SDK 的消息处理回调，responder 生命周期不应超过该 context。
//
// 活动消息采用流式累加器模式（参考 Telegram 的 EditMessage 方案）：
// 多个 activity 块累加为一条 GENERATING 消息，最终回复时用 FINISH 终结，
// 避免每个 activity 都发送独立消息导致对话刷屏。
type WeixinResponder struct {
	sharedResponderNoOps

	ctx context.Context
	bot *weixinbot.Bot
	msg *weixinbot.IncomingMessage

	mu           sync.Mutex
	streamCID    string // 活动流的 client_id，用于 GENERATING → FINISH 序列
	activityText string // 累加的活动文本
}

var _ domain.Responder = (*WeixinResponder)(nil)

// NewWeixinResponder creates a new WeixinResponder.
func NewWeixinResponder(ctx context.Context, bot *weixinbot.Bot, msg *weixinbot.IncomingMessage) *WeixinResponder {
	return &WeixinResponder{ctx: ctx, bot: bot, msg: msg}
}

// ChannelKind returns the channel kind.
func (r *WeixinResponder) ChannelKind() string { return "weixin" }

// Reply sends an outbound message to the user using SendRawMessage (Method B).
// 先终结活动流（GENERATING → FINISH），再逐段发送正文，最后清除 typing。
func (r *WeixinResponder) Reply(msg domain.OutboundMessage) error {
	if msg.Text == "" {
		return nil
	}
	r.mu.Lock()
	r.finishActivityStreamLocked()
	r.mu.Unlock()

	ct := r.msg.ContextToken()
	for _, chunk := range chunkRunes(msg.Text, weixinbot.MaxMessageTextRunes) {
		cid, _ := weixinbot.GenerateClientID()
		if err := r.bot.SendRawMessage(r.ctx, r.msg.UserID, chunk, ct, cid, weixinbot.MessageStateFinish); err != nil {
			return err
		}
	}
	_ = r.bot.StopTyping(r.ctx, r.msg.UserID)
	return nil
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

// SendActivity sends an agent activity block using the streaming accumulator.
// 多个 activity 累加为一条 GENERATING 消息（参考 Telegram 的 EditMessage 模式），
// 避免每个 tool call / thinking 都发送独立消息导致刷屏。
// 累加文本超限时，先 FINISH 当前流再开启新流。
func (r *WeixinResponder) SendActivity(block domain.ActivityBlock) error {
	line := block.FormatActivityText()
	if line == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activityText == "" {
		r.activityText = line
	} else if runeCount(r.activityText)+runeCount(line)+1 > activityAccumulatorLimit {
		r.finishActivityStreamLocked()
		r.activityText = line
	} else {
		r.activityText += "\n" + line
	}

	if r.streamCID == "" {
		cid, _ := weixinbot.GenerateClientID()
		r.streamCID = cid
	}

	ct := r.msg.ContextToken()
	_ = r.bot.SendRawMessage(r.ctx, r.msg.UserID, r.activityText, ct, r.streamCID, weixinbot.MessageStateGenerating)
	return nil
}

// finishActivityStreamLocked 终结当前活动流，将 GENERATING 切换为 FINISH。
// 调用方必须持有 r.mu。
func (r *WeixinResponder) finishActivityStreamLocked() {
	if r.streamCID == "" {
		return
	}
	if r.activityText != "" {
		ct := r.msg.ContextToken()
		_ = r.bot.SendRawMessage(r.ctx, r.msg.UserID, r.activityText, ct, r.streamCID, weixinbot.MessageStateFinish)
	}
	r.streamCID = ""
	r.activityText = ""
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

func runeCount(s string) int { return len([]rune(s)) }

// chunkRunes splits s into segments of at most limit UTF-8 code points.
func chunkRunes(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		n := 0
		runes := 0
		for runes < limit && n < len(s) {
			_, sz := utf8.DecodeRuneInString(s[n:])
			if sz == 0 {
				break
			}
			n += sz
			runes++
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}
