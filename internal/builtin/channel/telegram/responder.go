package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/domain"
)

// permissionAction maps ACP SDK permission decision strings to Telegram UI labels
// and callback data. Keys must match domain.PermissionDecision values from the SDK.
var permissionAction = map[string]struct {
	label string
	cb    string
}{
	"always":    {"Always", "always"},
	"this_time": {"This time", "once"},
	"deny":      {"Deny", "deny"},
}

const activityAccumulatorLimit = 4000

// TelegramResponder implements domain.Responder for Telegram.
// ctx is the request-scoped context from the incoming Telegram update handler.
// The responder must not outlive this context — all bot API calls use it.
type TelegramResponder struct {
	ctx    context.Context
	bot    *telego.Bot
	chatID int64
	msgID  int

	mu            sync.Mutex
	activityMsgID int
	activityText  string
}

var _ domain.Responder = (*TelegramResponder)(nil)

// NewTelegramResponder creates a new TelegramResponder.
func NewTelegramResponder(ctx context.Context, bot *telego.Bot, chatID int64, msgID int) *TelegramResponder {
	return &TelegramResponder{ctx: ctx, bot: bot, chatID: chatID, msgID: msgID}
}

// ChannelKind returns the channel kind.
func (r *TelegramResponder) ChannelKind() string { return "telegram" }

// BackgroundResponder implements domain.Responder for background tasks (e.g. cron).
// ctx is the application-level context; it lives for the duration of the process.
type BackgroundResponder struct {
	ctx    context.Context
	bot    *telego.Bot
	chatID int64
}

// NewBackgroundResponder creates a new BackgroundResponder.
func NewBackgroundResponder(ctx context.Context, bot *telego.Bot, chatID int64) *BackgroundResponder {
	return &BackgroundResponder{ctx: ctx, bot: bot, chatID: chatID}
}

// ChannelKind returns the channel kind.
func (r *BackgroundResponder) ChannelKind() string { return "telegram" }

// Reply sends an outbound message to the chat.
func (r *BackgroundResponder) Reply(msg domain.OutboundMessage) error {
	return sendOutbound(r.ctx, r.bot, r.chatID, msg)
}

func (r *BackgroundResponder) ShowPermissionUI(req domain.ChannelPermissionRequest) error { return nil }
func (r *BackgroundResponder) ShowTypingIndicator() error                                 { return nil }
func (r *BackgroundResponder) SendActivity(block domain.ActivityBlock) error              { return nil }
func (r *BackgroundResponder) ShowBusyNotification(token string, replyToMsgID int) (int, error) {
	return 0, nil
}
func (r *BackgroundResponder) ClearBusyNotification(notifyMsgID int) error              { return nil }
func (r *BackgroundResponder) ShowResumeKeyboard(sessions []domain.SessionChoice) error { return nil }

// Reply sends an outbound message to the chat.
func (r *TelegramResponder) Reply(msg domain.OutboundMessage) error {
	return sendOutbound(r.ctx, r.bot, r.chatID, msg)
}

// ShowPermissionUI sends an inline keyboard for permission approval.
func (r *TelegramResponder) ShowPermissionUI(req domain.ChannelPermissionRequest) error {
	var buttons []telego.InlineKeyboardButton
	for _, action := range req.AvailableActions {
		perm, ok := permissionAction[action]
		if !ok {
			continue
		}
		buttons = append(buttons, tu.InlineKeyboardButton(perm.label).WithCallbackData(fmt.Sprintf("perm|%s|%s", req.ID, perm.cb)))
	}
	keyboard := tu.InlineKeyboard(tu.InlineKeyboardRow(buttons...))

	text := "**⚠️ Permission required**"
	if req.Tool != "" {
		text += "\n\n" + req.Tool
	}
	return sendWithMarkdownFallback(r.ctx, r.bot, r.chatID, text, keyboard)
}

// sendWithMarkdownFallback sends a message using MarkdownV2, falling back to plain text on failure.
func sendWithMarkdownFallback(ctx context.Context, bot *telego.Bot, chatID int64, text string, keyboard *telego.InlineKeyboardMarkup) error {
	plainParams := tu.Message(tu.ID(chatID), text)
	if keyboard != nil {
		plainParams = plainParams.WithReplyMarkup(keyboard)
	}
	chunks := RenderMarkdown(text)
	if len(chunks) == 0 {
		_, err := bot.SendMessage(ctx, plainParams)
		return err
	}
	mdParams := tu.Message(tu.ID(chatID), chunks[0].Text).WithParseMode(telego.ModeMarkdownV2)
	if keyboard != nil {
		mdParams = mdParams.WithReplyMarkup(keyboard)
	}
	if _, err := bot.SendMessage(ctx, mdParams); err == nil {
		return nil
	}
	if _, err := bot.SendMessage(ctx, plainParams); err != nil {
		slog.Error("failed to send message", "chat_id", chatID, "error", err)
		return err
	}
	return nil
}

// ShowTypingIndicator sends a typing chat action.
func (r *TelegramResponder) ShowTypingIndicator() error {
	return r.bot.SendChatAction(r.ctx, &telego.SendChatActionParams{
		ChatID: tu.ID(r.chatID),
		Action: "typing",
	})
}

// SendActivity sends an agent activity block as a message.
func (r *TelegramResponder) SendActivity(block domain.ActivityBlock) error {
	line := buildActivityLine(block)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.mustSendNewActivity(line) {
		return r.sendNewActivityMessage(line)
	}

	r.activityText = r.activityText + "\n" + line
	if err := r.updateActivityMessage(); err != nil {
		return r.sendNewActivityMessage(line)
	}
	return nil
}

func (r *TelegramResponder) mustSendNewActivity(line string) bool {
	if r.activityMsgID == 0 {
		return true
	}
	return runeCount(r.activityText+"\n"+line) > activityAccumulatorLimit
}

func (r *TelegramResponder) updateActivityMessage() error {
	chunks := RenderMarkdown(r.activityText)
	if len(chunks) == 0 {
		return nil
	}
	_, err := r.bot.EditMessageText(r.ctx, &telego.EditMessageTextParams{
		ChatID:    tu.ID(r.chatID),
		MessageID: r.activityMsgID,
		Text:      chunks[0].Text,
		ParseMode: telego.ModeMarkdownV2,
	})
	return err
}

func buildActivityLine(block domain.ActivityBlock) string {
	switch block.Kind {
	case domain.ActivityThink:
		return buildThinkActivityLine(block)
	case domain.ActivityExecute:
		return formatActivityMessage(block)
	default:
		return formatActivityLine(block)
	}
}

func buildThinkActivityLine(block domain.ActivityBlock) string {
	text := truncateRunes(block.Text, maxThinkTextRunes)
	if text == "" {
		return "**" + block.Label + "**"
	}
	return "**" + block.Label + "**\n" + text
}

func (r *TelegramResponder) sendNewActivityMessage(text string) error {
	if text == "" {
		return nil
	}
	r.activityText = text

	msgText, useMarkdown := pickActivityMessageText(text)
	params := tu.Message(tu.ID(r.chatID), msgText)
	if useMarkdown {
		params = params.WithParseMode(telego.ModeMarkdownV2)
	}
	sent, err := r.bot.SendMessage(r.ctx, params)
	if err != nil && useMarkdown {
		sent, err = r.bot.SendMessage(r.ctx, tu.Message(tu.ID(r.chatID), text))
	}
	if err != nil {
		r.activityMsgID = 0
		return err
	}
	r.activityMsgID = sent.MessageID
	return nil
}

func pickActivityMessageText(text string) (string, bool) {
	chunks := RenderMarkdown(text)
	if len(chunks) == 0 {
		return text, false
	}
	return chunks[0].Text, true
}

// ShowBusyNotification sends a "queued" notification with a "Send now" button.
func (r *TelegramResponder) ShowBusyNotification(token string, replyToMsgID int) (int, error) {
	keyboard := tu.InlineKeyboard(tu.InlineKeyboardRow(
		tu.InlineKeyboardButton("Send now").WithCallbackData("busy|" + token),
	))
	params := tu.Message(tu.ID(r.chatID), "⏳ Agent is busy. Your message is queued.").WithReplyMarkup(keyboard)
	if replyToMsgID > 0 {
		params.ReplyParameters = &telego.ReplyParameters{MessageID: replyToMsgID}
	}
	sent, err := r.bot.SendMessage(r.ctx, params)
	if err != nil {
		return 0, err
	}
	return sent.MessageID, nil
}

// ClearBusyNotification removes the inline keyboard from a busy notification.
func (r *TelegramResponder) ClearBusyNotification(notifyMsgID int) error {
	if notifyMsgID == 0 {
		return nil
	}
	_, err := r.bot.EditMessageReplyMarkup(r.ctx, &telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(r.chatID),
		MessageID:   notifyMsgID,
		ReplyMarkup: tu.InlineKeyboard(),
	})
	return err
}

const maxSessionLabelLen = 48

// ShowResumeKeyboard sends an inline keyboard for session selection.
func (r *TelegramResponder) ShowResumeKeyboard(sessions []domain.SessionChoice) error {
	var rows [][]telego.InlineKeyboardButton
	for _, s := range sessions {
		label := truncate(fmt.Sprintf("%d. %s", s.Index+1, s.DisplayName), maxSessionLabelLen)
		rows = append(rows, tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(label).WithCallbackData(fmt.Sprintf("resume|%d", s.Index)),
		))
	}
	_, err := r.bot.SendMessage(r.ctx,
		tu.Message(tu.ID(r.chatID), "Pick a session to resume:").WithReplyMarkup(tu.InlineKeyboard(rows...)))
	return err
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// --- Activity formatting ---

const maxThinkTextRunes = 100

func runeCount(s string) int { return len([]rune(s)) }

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func formatActivityLine(block domain.ActivityBlock) string {
	detailParts, detail := formatActivityDetail(block)
	parts := []string{block.Label}
	parts = append(parts, detailParts...)
	if detail != "" && detail != block.Label {
		parts = append(parts, detail)
	}
	return strings.Join(parts, " ")
}

var localWorkspaceWordPattern = regexp.MustCompile(
	`\b(workspace|repository|repo|project|ripgrep|rg|grep|glob)\b`,
)

var webHints = []string{"http://", "https://", "url:", "web search", "internet"}

func searchSourceLabel(title, text string) string {
	content := strings.ToLower(title + "\n" + text)
	for _, h := range webHints {
		if strings.Contains(content, h) {
			return "🌐 Searching web"
		}
	}
	if strings.Contains(content, "file://") || localWorkspaceWordPattern.MatchString(content) {
		return "🔎 Querying project"
	}
	return ""
}

func maxBacktickRun(s string) int {
	var maxRun, run int
	for _, ch := range s {
		if ch == '`' {
			run++
			maxRun = max(maxRun, run)
		} else {
			run = 0
		}
	}
	return maxRun
}

func fencedCode(text string) string {
	fence := strings.Repeat("`", max(3, maxBacktickRun(text)+1))
	return fence + "\n" + text + "\n" + fence
}

func formatRunCommands(detail string) ([]string, bool) {
	if !strings.HasPrefix(detail, "Run ") {
		return nil, false
	}
	cmd := strings.TrimPrefix(detail, "Run ")
	rawCommands := strings.Split(cmd, ", Run ")
	var result []string
	for _, c := range rawCommands {
		c = strings.TrimSpace(c)
		if c != "" {
			result = append(result, fencedCode(c))
		}
	}
	return result, true
}

func formatActivityPath(raw, workspace string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "file://"); idx >= 0 {
		if u, err := url.Parse(raw[idx:]); err == nil && u.Scheme == "file" && u.Path != "" {
			raw = strings.TrimRight(u.Path, ")")
		}
	}
	if workspace != "" {
		raw = strings.TrimPrefix(raw, workspace+"/")
	}
	return raw
}

var pathDetailPrefixes = map[domain.ActivityKind]string{
	domain.ActivityRead:  "Read ",
	domain.ActivityEdit:  "Edit ",
	domain.ActivityWrite: "Write ",
}

func formatActivityDetail(block domain.ActivityBlock) ([]string, string) {
	detail := block.Detail
	switch block.Kind {
	case domain.ActivityExecute:
		if runParts, ok := formatRunCommands(detail); ok {
			return runParts, ""
		}
	case domain.ActivityRead, domain.ActivityEdit, domain.ActivityWrite:
		if prefix := pathDetailPrefixes[block.Kind]; strings.HasPrefix(detail, prefix) {
			path := formatActivityPath(strings.TrimPrefix(detail, prefix), block.Workspace)
			return []string{"`" + path + "`"}, ""
		}
	case domain.ActivitySearch:
		if detail != "" && detail != block.Label {
			return []string{"`" + truncateRunes(detail, 60) + "`"}, ""
		}
	}
	return nil, detail
}

func formatActivityMessage(block domain.ActivityBlock) string {
	label := block.Label
	if block.Kind == domain.ActivitySearch {
		if alt := searchSourceLabel(block.Detail, block.Text); alt != "" {
			label = alt
		}
	}
	parts := []string{"**" + label + "**"}

	detailParts, detail := formatActivityDetail(block)
	parts = append(parts, detailParts...)

	if detail != "" && detail != block.Label {
		parts = append(parts, detail)
	}

	text := block.Text
	if block.Kind == domain.ActivityThink && text != "" {
		text = truncateRunes(text, maxThinkTextRunes)
	}
	if text != "" && text != block.Detail && text != block.Label {
		parts = append(parts, text)
	}
	if block.Status == "failed" {
		parts = append(parts, "_Failed_")
	}
	return strings.Join(parts, "\n\n")
}
