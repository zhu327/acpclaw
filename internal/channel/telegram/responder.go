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

// permissionAction maps internal action IDs to display labels and callback data.
var permissionAction = map[string]struct {
	label string
	cb    string
}{
	"always_allow": {"Always", "always"},
	"allow_once":   {"This time", "once"},
	"deny":         {"Deny", "deny"},
}

const activityAccumulatorLimit = 4000

// TelegramResponder implements domain.Responder for Telegram.
type TelegramResponder struct {
	bot    *telego.Bot
	chatID int64
	msgID  int

	mu            sync.Mutex
	activityMsgID int
	activityText  string
}

var _ domain.Responder = (*TelegramResponder)(nil)

// NewTelegramResponder creates a new TelegramResponder.
func NewTelegramResponder(bot *telego.Bot, chatID int64, msgID int) *TelegramResponder {
	return &TelegramResponder{bot: bot, chatID: chatID, msgID: msgID}
}

// BackgroundResponder implements domain.Responder for background tasks.
type BackgroundResponder struct {
	bot    *telego.Bot
	chatID int64
}

// NewBackgroundResponder creates a new BackgroundResponder.
func NewBackgroundResponder(bot *telego.Bot, chatID int64) *BackgroundResponder {
	return &BackgroundResponder{bot: bot, chatID: chatID}
}

// Reply sends an outbound message to the chat.
func (r *BackgroundResponder) Reply(msg domain.OutboundMessage) error {
	return sendOutbound(r.bot, r.chatID, msg)
}

func (r *BackgroundResponder) ShowPermissionUI(req domain.ChannelPermissionRequest) error {
	return nil
}
func (r *BackgroundResponder) ShowTypingIndicator() error { return nil }
func (r *BackgroundResponder) SendActivity(block domain.ActivityBlock) error {
	return nil
}

func (r *BackgroundResponder) ShowBusyNotification(token string, replyToMsgID int) (int, error) {
	return 0, nil
}
func (r *BackgroundResponder) ClearBusyNotification(notifyMsgID int) error { return nil }
func (r *BackgroundResponder) ShowResumeKeyboard(sessions []domain.SessionChoice) error {
	return nil
}

// Reply sends an outbound message to the chat.
func (r *TelegramResponder) Reply(msg domain.OutboundMessage) error {
	return sendOutbound(r.bot, r.chatID, msg)
}

// ShowPermissionUI sends an inline keyboard for permission approval.
func (r *TelegramResponder) ShowPermissionUI(req domain.ChannelPermissionRequest) error {
	var row []telego.InlineKeyboardButton
	for _, action := range req.AvailableActions {
		pa, ok := permissionAction[action]
		if !ok {
			continue
		}
		row = append(row, tu.InlineKeyboardButton(pa.label).WithCallbackData(fmt.Sprintf("perm|%s|%s", req.ID, pa.cb)))
	}
	keyboard := tu.InlineKeyboard(tu.InlineKeyboardRow(row...))

	text := "**⚠️ Permission required**"
	if req.Tool != "" {
		text += "\n\n" + req.Tool
	}
	return sendWithMarkdownFallback(r.bot, r.chatID, text, keyboard)
}

// sendWithMarkdownFallback sends a message using MarkdownV2, falling back to plain text on failure.
func sendWithMarkdownFallback(bot *telego.Bot, chatID int64, text string, keyboard *telego.InlineKeyboardMarkup) error {
	chunks := RenderMarkdown(text)
	plainParams := tu.Message(tu.ID(chatID), text)
	if keyboard != nil {
		plainParams = plainParams.WithReplyMarkup(keyboard)
	}
	if len(chunks) == 0 {
		_, err := bot.SendMessage(context.TODO(), plainParams)
		return err
	}
	mdParams := tu.Message(tu.ID(chatID), chunks[0].Text).WithParseMode(telego.ModeMarkdownV2)
	if keyboard != nil {
		mdParams = mdParams.WithReplyMarkup(keyboard)
	}
	if _, err := bot.SendMessage(context.TODO(), mdParams); err != nil {
		if _, err2 := bot.SendMessage(context.TODO(), plainParams); err2 != nil {
			slog.Error("failed to send message", "chat_id", chatID, "error", err2)
			return err2
		}
	}
	return nil
}

// ShowTypingIndicator sends a typing chat action.
func (r *TelegramResponder) ShowTypingIndicator() error {
	return r.bot.SendChatAction(context.TODO(), &telego.SendChatActionParams{
		ChatID: tu.ID(r.chatID),
		Action: "typing",
	})
}

// SendActivity sends an agent activity block as a message.
func (r *TelegramResponder) SendActivity(block domain.ActivityBlock) error {
	var line string
	switch block.Kind {
	case domain.ActivityThink:
		text := block.Text
		if text != "" {
			text = truncateRunes(text, maxThinkTextRunes)
		}
		line = "**" + block.Label + "**"
		if text != "" {
			line += "\n" + text
		}
	case domain.ActivityExecute:
		line = formatActivityMessage(block)
	default:
		line = formatActivityLine(block)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activityMsgID == 0 {
		return r.sendNewActivityMessage(line)
	}

	newText := r.activityText + "\n" + line
	if len([]rune(newText)) > activityAccumulatorLimit {
		return r.sendNewActivityMessage(line)
	}

	r.activityText = newText
	chunks := RenderMarkdown(r.activityText)
	if len(chunks) == 0 {
		return nil
	}

	_, err := r.bot.EditMessageText(context.TODO(), &telego.EditMessageTextParams{
		ChatID:    tu.ID(r.chatID),
		MessageID: r.activityMsgID,
		Text:      chunks[0].Text,
		ParseMode: telego.ModeMarkdownV2,
	})
	if err != nil {
		return r.sendNewActivityMessage(line)
	}
	return nil
}

func (r *TelegramResponder) sendNewActivityMessage(text string) error {
	if text == "" {
		return nil
	}
	r.activityText = text

	chunks := RenderMarkdown(text)
	var params *telego.SendMessageParams
	if len(chunks) > 0 {
		params = tu.Message(tu.ID(r.chatID), chunks[0].Text).WithParseMode(telego.ModeMarkdownV2)
	} else {
		params = tu.Message(tu.ID(r.chatID), text)
	}

	sent, err := r.bot.SendMessage(context.TODO(), params)
	if err != nil && len(chunks) > 0 {
		sent, err = r.bot.SendMessage(context.TODO(), tu.Message(tu.ID(r.chatID), text))
	}
	if err != nil {
		r.activityMsgID = 0
		return err
	}
	r.activityMsgID = sent.MessageID
	return nil
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
	sent, err := r.bot.SendMessage(context.TODO(), params)
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
	_, err := r.bot.EditMessageReplyMarkup(context.TODO(), &telego.EditMessageReplyMarkupParams{
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
	_, err := r.bot.SendMessage(context.TODO(),
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

var searchLocalWordBoundary = regexp.MustCompile(
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
	if strings.Contains(content, "file://") || searchLocalWordBoundary.MatchString(content) {
		return "🔎 Querying project"
	}
	return ""
}

func maxBacktickRun(s string) int {
	maxRun, run := 0, 0
	for _, ch := range s {
		if ch == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
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
	parts := strings.Split(cmd, ", Run ")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, fencedCode(p))
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

func formatActivityDetail(block domain.ActivityBlock) ([]string, string) {
	detail := block.Detail
	switch block.Kind {
	case "execute":
		if runParts, ok := formatRunCommands(detail); ok {
			return runParts, ""
		}
	case "read", "edit":
		prefix := "Read "
		if block.Kind == "edit" {
			prefix = "Edit "
		}
		if strings.HasPrefix(detail, prefix) {
			path := formatActivityPath(strings.TrimPrefix(detail, prefix), block.Workspace)
			return []string{"`" + path + "`"}, ""
		}
	}
	return nil, detail
}

func formatActivityMessage(block domain.ActivityBlock) string {
	label := block.Label
	if block.Kind == "search" {
		if sl := searchSourceLabel(block.Detail, block.Text); sl != "" {
			label = sl
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
