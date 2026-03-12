package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/channel"
)

// TelegramResponder implements channel.Responder for Telegram.
type TelegramResponder struct {
	bot    *telego.Bot
	chatID int64
	msgID  int
}

var _ channel.Responder = (*TelegramResponder)(nil)

// NewTelegramResponder creates a new TelegramResponder.
func NewTelegramResponder(bot *telego.Bot, chatID int64, msgID int) *TelegramResponder {
	return &TelegramResponder{bot: bot, chatID: chatID, msgID: msgID}
}

// Reply sends an outbound message to the chat.
func (r *TelegramResponder) Reply(msg channel.OutboundMessage) error {
	return sendOutbound(r.bot, r.chatID, msg)
}

// ShowPermissionUI sends an inline keyboard for permission approval.
func (r *TelegramResponder) ShowPermissionUI(req channel.PermissionRequest) error {
	var row []telego.InlineKeyboardButton
	labels := map[string]string{
		"always_allow": "Always",
		"allow_once":   "This time",
		"deny":         "Deny",
	}
	callbackActions := map[string]string{
		"always_allow": "always",
		"allow_once":   "once",
		"deny":         "deny",
	}
	for _, action := range req.AvailableActions {
		label, ok := labels[action]
		if !ok {
			continue
		}
		cbAction := callbackActions[action]
		data := fmt.Sprintf("perm|%s|%s", req.ID, cbAction)
		row = append(row, tu.InlineKeyboardButton(label).WithCallbackData(data))
	}
	keyboard := tu.InlineKeyboard(tu.InlineKeyboardRow(row...))

	text := "**⚠️ Permission required**"
	if req.Tool != "" {
		text += "\n\n" + req.Tool
	}

	chunks := RenderMarkdown(text)
	if len(chunks) == 0 {
		_, err := r.bot.SendMessage(context.TODO(), tu.Message(tu.ID(r.chatID), text).WithReplyMarkup(keyboard))
		return err
	}
	params := &telego.SendMessageParams{
		ChatID:      tu.ID(r.chatID),
		Text:        chunks[0].Text,
		ParseMode:   telego.ModeMarkdownV2,
		ReplyMarkup: keyboard,
	}
	if _, err := r.bot.SendMessage(context.TODO(), params); err != nil {
		if _, err2 := r.bot.SendMessage(context.TODO(), tu.Message(tu.ID(r.chatID), text).WithReplyMarkup(keyboard)); err2 != nil {
			slog.Error("failed to send permission request", "chat_id", r.chatID, "error", err2)
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
func (r *TelegramResponder) SendActivity(block channel.ActivityBlock) error {
	text := formatActivityMessage(block)
	chunks := RenderMarkdown(text)
	for _, chunk := range chunks {
		params := tu.Message(tu.ID(r.chatID), chunk.Text).WithParseMode(telego.ModeMarkdownV2)
		if _, err := r.bot.SendMessage(context.TODO(), params); err != nil {
			_, _ = r.bot.SendMessage(context.TODO(), tu.Message(tu.ID(r.chatID), text))
			break
		}
	}
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

// ShowResumeKeyboard sends an inline keyboard for session selection.
func (r *TelegramResponder) ShowResumeKeyboard(sessions []channel.SessionChoice) error {
	var rows [][]telego.InlineKeyboardButton
	for _, s := range sessions {
		label := fmt.Sprintf("%d. %s", s.Index+1, s.DisplayName)
		if len(label) > 48 {
			label = label[:48]
		}
		rows = append(rows, tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(label).WithCallbackData(fmt.Sprintf("resume|%d", s.Index)),
		))
	}
	keyboard := tu.InlineKeyboard(rows...)
	params := tu.Message(tu.ID(r.chatID), "Pick a session to resume:").WithReplyMarkup(keyboard)
	_, err := r.bot.SendMessage(context.TODO(), params)
	return err
}

// --- Activity formatting ---

var searchLocalWordBoundary = regexp.MustCompile(
	`\b(workspace|repository|repo|project|ripgrep|rg|grep|glob)\b`,
)

func searchSourceLabel(title, text string) string {
	content := strings.ToLower(title + "\n" + text)
	webHints := []string{"http://", "https://", "url:", "web search", "internet"}
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

func fencedCode(text string) string {
	maxRun, run := 0, 0
	for _, ch := range text {
		if ch == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, maxRun+1))
	return fence + "\n" + text + "\n" + fence
}

func formatRunCommands(detail string) ([]string, bool) {
	if !strings.HasPrefix(detail, "Run ") {
		return nil, false
	}
	cmd := strings.TrimPrefix(detail, "Run ")
	if strings.Contains(cmd, ", Run ") {
		var parts []string
		for _, c := range strings.Split(cmd, ", Run ") {
			c = strings.TrimSpace(c)
			if c != "" {
				parts = append(parts, fencedCode(c))
			}
		}
		return parts, true
	}
	return []string{fencedCode(cmd)}, true
}

func formatActivityPath(raw, workspace string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "file://"); idx >= 0 {
		u, err := url.Parse(raw[idx:])
		if err == nil && u.Scheme == "file" && u.Path != "" {
			raw = strings.TrimRight(u.Path, ")")
		}
	}
	if workspace != "" {
		raw = strings.TrimPrefix(raw, workspace+"/")
	}
	return raw
}

func formatActivityMessage(block channel.ActivityBlock) string {
	label := block.Label
	if block.Kind == "search" {
		if sl := searchSourceLabel(block.Detail, block.Text); sl != "" {
			label = sl
		}
	}
	parts := []string{"**" + label + "**"}

	detail := block.Detail
	switch block.Kind {
	case "execute":
		if runParts, ok := formatRunCommands(detail); ok {
			parts = append(parts, runParts...)
			detail = ""
		}
	case "read", "edit":
		prefix := map[string]string{"read": "Read ", "edit": "Edit "}[block.Kind]
		if strings.HasPrefix(detail, prefix) {
			path := formatActivityPath(strings.TrimPrefix(detail, prefix), block.Workspace)
			parts = append(parts, "`"+path+"`")
			detail = ""
		}
	}

	if detail != "" && detail != block.Label {
		parts = append(parts, detail)
	}
	if block.Text != "" && block.Text != block.Detail && block.Text != block.Label {
		parts = append(parts, block.Text)
	}
	if block.Status == "failed" {
		parts = append(parts, "_Failed_")
	}
	return strings.Join(parts, "\n\n")
}
