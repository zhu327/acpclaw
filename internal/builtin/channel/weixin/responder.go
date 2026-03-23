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

// activityAccumulatorLimit 限制单条活动摘要消息长度（rune 数），为 MaxMessageTextRunes 留余量。
const activityAccumulatorLimit = 1800

// WeixinResponder implements domain.Responder for WeChat.
// ctx 来自 SDK 的消息处理回调，responder 生命周期不应超过该 context。
//
// 活动消息采用静默累加模式（参考 Telegram 的 EditMessage 方案）：
// 处理期间在内存中累加 activity 行，Reply 时一次性发送合并的活动摘要，
// 避免每个 activity 都发送独立消息导致对话刷屏。
// 不使用 GENERATING state，因为微信客户端仅渲染同一 client_id 的首条 GENERATING，
// 后续更新不生效。
type WeixinResponder struct {
	sharedResponderNoOps

	ctx context.Context
	bot *weixinbot.Bot
	msg *weixinbot.IncomingMessage

	mu             sync.Mutex
	activityChunks []string // 累加的活动摘要分段（每段不超过 activityAccumulatorLimit）
	currentChunk   string   // 当前正在累加的分段
}

var _ domain.Responder = (*WeixinResponder)(nil)

// NewWeixinResponder creates a new WeixinResponder.
func NewWeixinResponder(ctx context.Context, bot *weixinbot.Bot, msg *weixinbot.IncomingMessage) *WeixinResponder {
	return &WeixinResponder{ctx: ctx, bot: bot, msg: msg}
}

// ChannelKind returns the channel kind.
func (r *WeixinResponder) ChannelKind() string { return "weixin" }

// Reply sends an outbound message to the user using SendRawMessage (Method B).
// 先发送累加的活动摘要，再逐段发送正文，最后清除 typing。
func (r *WeixinResponder) Reply(msg domain.OutboundMessage) error {
	if msg.Text == "" {
		return nil
	}
	r.mu.Lock()
	r.flushActivityLocked()
	activityChunks := r.activityChunks
	r.activityChunks = nil
	r.mu.Unlock()

	ct := r.msg.ContextToken()

	for _, text := range activityChunks {
		cid, _ := weixinbot.GenerateClientID()
		if err := r.bot.SendRawMessage(r.ctx, r.msg.UserID, text, ct, cid, weixinbot.MessageStateFinish); err != nil {
			return err
		}
	}

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

// SendActivity accumulates an agent activity block into the in-memory buffer.
// 处理期间不发送任何消息（typing 已提供实时反馈），Reply 时一次性发送合并的活动摘要。
// 当累加文本超过 activityAccumulatorLimit 时自动分段。
func (r *WeixinResponder) SendActivity(block domain.ActivityBlock) error {
	line := block.FormatActivityText()
	if line == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentChunk == "" {
		r.currentChunk = line
	} else if runeCount(r.currentChunk)+runeCount(line)+1 > activityAccumulatorLimit {
		r.flushActivityLocked()
		r.currentChunk = line
	} else {
		r.currentChunk += "\n" + line
	}
	return nil
}

// flushActivityLocked 将当前累加分段存入 activityChunks 列表。
// 调用方必须持有 r.mu。
func (r *WeixinResponder) flushActivityLocked() {
	if r.currentChunk == "" {
		return
	}
	r.activityChunks = append(r.activityChunks, r.currentChunk)
	r.currentChunk = ""
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
