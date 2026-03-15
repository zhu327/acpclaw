package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/domain"
)

// ChannelConfig holds Telegram-specific configuration.
type ChannelConfig struct {
	AllowedUserIDs   []int64
	AllowedUsernames []string
}

// FrameworkCallbacks is the interface for Framework callback handling.
// Implemented by *framework.Framework; channel uses it for permission, busy, resume.
type FrameworkCallbacks interface {
	RespondPermission(reqID string, decision domain.PermissionDecision)
	HandleBusySendNow(chat domain.ChatRef, token string) (bool, error)
	ResolveResumeChoice(ctx context.Context, chat domain.ChatRef, sessionIndex int) (*domain.SessionInfo, error)
}

// TelegramChannel implements domain.Channel for the Telegram platform.
type TelegramChannel struct {
	bot              *telego.Bot
	handler          *th.BotHandler
	cfg              ChannelConfig
	updates          <-chan telego.Update
	callbacks        FrameworkCallbacks
	allowlistChecker AllowlistChecker
}

var _ domain.Channel = (*TelegramChannel)(nil)

// NewTelegramChannel creates a new TelegramChannel.
func NewTelegramChannel(
	bot *telego.Bot,
	updates <-chan telego.Update,
	cfg ChannelConfig,
	callbacks FrameworkCallbacks,
	allowlistChecker AllowlistChecker,
) *TelegramChannel {
	return &TelegramChannel{
		bot:              bot,
		updates:          updates,
		cfg:              cfg,
		callbacks:        callbacks,
		allowlistChecker: allowlistChecker,
	}
}

// Kind returns the channel kind identifier.
func (c *TelegramChannel) Kind() string { return "telegram" }

// Start registers handlers and begins processing updates.
func (c *TelegramChannel) Start(ctx context.Context, handler domain.MessageHandler) error {
	var err error
	c.handler, err = th.NewBotHandler(c.bot, c.updates)
	if err != nil {
		return fmt.Errorf("create bot handler: %w", err)
	}

	c.handler.HandleCallbackQuery(func(thCtx *th.Context, query telego.CallbackQuery) error {
		return c.handlePermissionCallback(thCtx, query)
	}, th.CallbackDataPrefix("perm|"))

	c.handler.HandleCallbackQuery(func(thCtx *th.Context, query telego.CallbackQuery) error {
		return c.handleBusyCallback(thCtx, query)
	}, th.CallbackDataPrefix("busy|"))

	c.handler.HandleCallbackQuery(func(thCtx *th.Context, query telego.CallbackQuery) error {
		return c.handleResumeCallback(thCtx, query)
	}, th.CallbackDataPrefix("resume|"))

	c.handler.HandleMessage(func(thCtx *th.Context, msg telego.Message) error {
		if msg.From == nil {
			return nil
		}
		if !c.isAllowed(msg.From.ID, msg.From.Username) {
			c.sendPlainText(ctx, msg.Chat.ID, "Access denied for this bot.")
			return nil
		}

		inbound := c.convertInbound(thCtx, msg)
		if inbound.Text == "" && len(inbound.Attachments) == 0 {
			return nil
		}

		resp := NewTelegramResponder(ctx, c.bot, msg.Chat.ID, msg.MessageID)
		handler(ctx, inbound, resp)
		return nil
	}, th.AnyMessage())

	return c.handler.Start()
}

// Stop halts the bot handler.
func (c *TelegramChannel) Stop() error {
	if c.handler != nil {
		_ = c.handler.Stop()
	}
	return nil
}

func (c *TelegramChannel) convertInbound(ctx *th.Context, msg telego.Message) domain.InboundMessage {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}

	inbound := domain.InboundMessage{
		ChatRef: domain.ChatRef{ChannelKind: "telegram", ChatID: strconv.FormatInt(msg.Chat.ID, 10)},
		ID:      strconv.Itoa(msg.MessageID),
		Text:    text,
	}
	if msg.From != nil {
		inbound.AuthorID = strconv.FormatInt(msg.From.ID, 10)
		inbound.AuthorName = msg.From.FirstName
	}

	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		if data, err := c.downloadFile(ctx.Context(), photo.FileID); err == nil {
			inbound.Attachments = append(inbound.Attachments, domain.Attachment{
				Data:      data,
				MediaType: "image",
				FileName:  "photo.jpg",
			})
		} else {
			slog.Error("failed to download photo", "error", err)
		}
	}

	if msg.Document != nil {
		if data, err := c.downloadFile(ctx.Context(), msg.Document.FileID); err == nil {
			mediaType := "file"
			if strings.HasPrefix(msg.Document.MimeType, "image/") {
				mediaType = "image"
			}
			inbound.Attachments = append(inbound.Attachments, domain.Attachment{
				Data:      data,
				MediaType: mediaType,
				FileName:  msg.Document.FileName,
			})
		} else {
			slog.Error("failed to download document", "error", err)
		}
	}

	return inbound
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	return tu.DownloadFile(c.bot.FileDownloadURL(file.FilePath))
}

func (c *TelegramChannel) handlePermissionCallback(ctx *th.Context, query telego.CallbackQuery) error {
	parts := strings.SplitN(query.Data, "|", 3)
	if len(parts) != 3 {
		return nil
	}
	if !c.checkCallbackAccess(ctx.Context(), query) {
		return nil
	}

	reqID := parts[1]
	decisionStr := parts[2]

	decision := mapCallbackToPermissionDecision(decisionStr)
	if c.callbacks != nil {
		c.callbacks.RespondPermission(reqID, decision)
	}

	labels := map[string]string{
		"always": "Approved for this session.",
		"once":   "Approved this time.",
		"deny":   "Denied.",
	}
	label := labels[decisionStr]
	if label == "" {
		label = "Request expired."
	}
	c.answerCallback(ctx.Context(), query, label)

	if query.Message != nil {
		chatID := query.Message.GetChat().ID
		msgID := query.Message.GetMessageID()
		originalText := ""
		if m, ok := query.Message.(*telego.Message); ok && m != nil {
			originalText = m.Text
		}
		_, _ = c.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
			ChatID:    tu.ID(chatID),
			MessageID: msgID,
			Text:      originalText + "\nDecision: " + label,
		})
	}
	return nil
}

func mapCallbackToPermissionDecision(s string) domain.PermissionDecision {
	switch s {
	case "always":
		return domain.PermissionAlways
	case "once":
		return domain.PermissionThisTime
	case "deny":
		return domain.PermissionDeny
	default:
		return domain.PermissionDeny
	}
}

func chatRefFromTelegramID(chatID int64) domain.ChatRef {
	return domain.ChatRef{
		ChannelKind: "telegram",
		ChatID:      strconv.FormatInt(chatID, 10),
	}
}

func (c *TelegramChannel) handleBusyCallback(ctx *th.Context, query telego.CallbackQuery) error {
	if !c.checkCallbackAccess(ctx.Context(), query) {
		return nil
	}

	token := strings.TrimPrefix(query.Data, "busy|")
	chatID := getChatIDFromQuery(query)
	chat := chatRefFromTelegramID(chatID)

	if c.callbacks == nil {
		c.answerCallback(ctx.Context(), query, "Already sent.")
		return nil
	}
	ok, err := c.callbacks.HandleBusySendNow(chat, token)
	if err != nil {
		c.answerCallback(ctx.Context(), query, "Cancel failed.")
		c.clearCallbackReplyMarkup(ctx.Context(), query)
		return nil
	}
	if !ok {
		c.answerCallback(ctx.Context(), query, "Already sent.")
		c.clearCallbackReplyMarkup(ctx.Context(), query)
		return nil
	}

	c.answerCallback(ctx.Context(), query, "✅ Sent.")
	if query.Message != nil {
		msgID := query.Message.GetMessageID()
		_, _ = c.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
			ChatID:    tu.ID(chatID),
			MessageID: msgID,
			Text:      "✅ Sent.",
		})
		_, _ = c.bot.EditMessageReplyMarkup(ctx.Context(), &telego.EditMessageReplyMarkupParams{
			ChatID:      tu.ID(chatID),
			MessageID:   msgID,
			ReplyMarkup: tu.InlineKeyboard(),
		})
	}
	return nil
}

func (c *TelegramChannel) handleResumeCallback(ctx *th.Context, query telego.CallbackQuery) error {
	if !c.checkCallbackAccess(ctx.Context(), query) {
		return nil
	}
	indexStr := strings.TrimPrefix(query.Data, "resume|")
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		c.answerCallback(ctx.Context(), query, "Invalid selection.")
		return nil
	}

	chatID := getChatIDFromQuery(query)
	chat := chatRefFromTelegramID(chatID)

	if c.callbacks == nil {
		c.answerCallback(ctx.Context(), query, "Selection expired.")
		return nil
	}

	s, err := c.callbacks.ResolveResumeChoice(ctx.Context(), chat, index)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "expired") {
			c.answerCallback(ctx.Context(), query, "Selection expired.")
		} else if strings.Contains(errMsg, "invalid") {
			c.answerCallback(ctx.Context(), query, "Invalid selection.")
		} else {
			c.answerCallback(ctx.Context(), query, "Failed to resume.")
			c.sendPlainText(ctx.Context(), chatID, "❌ Failed to resume session.")
		}
		return nil
	}

	c.answerCallback(ctx.Context(), query, "Session resumed.")
	if query.Message != nil {
		msgID := query.Message.GetMessageID()
		_, _ = c.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
			ChatID:    tu.ID(chatID),
			MessageID: msgID,
			Text:      fmt.Sprintf("Resumed session: %s\nWorkspace: %s", s.SessionID, s.Workspace),
		})
	}
	sendOutboundBestEffort(ctx.Context(), c.bot, chatID, domain.OutboundMessage{
		Text: fmt.Sprintf("Session resumed: `%s` in `%s`", s.SessionID, s.Workspace),
	})
	return nil
}

func getChatIDFromQuery(query telego.CallbackQuery) int64 {
	if query.Message != nil {
		if id := query.Message.GetChat().ID; id != 0 {
			return id
		}
	}
	return query.From.ID
}

func (c *TelegramChannel) answerCallback(ctx context.Context, query telego.CallbackQuery, text string) {
	_ = c.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID).WithText(text))
}

func (c *TelegramChannel) checkCallbackAccess(ctx context.Context, query telego.CallbackQuery) bool {
	if query.From.ID == 0 || !c.isAllowed(query.From.ID, query.From.Username) {
		c.answerCallback(ctx, query, "Access denied.")
		return false
	}
	return true
}

func (c *TelegramChannel) clearCallbackReplyMarkup(ctx context.Context, query telego.CallbackQuery) {
	if query.Message == nil {
		return
	}
	chatID := getChatIDFromQuery(query)
	_, _ = c.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(chatID),
		MessageID:   query.Message.GetMessageID(),
		ReplyMarkup: tu.InlineKeyboard(),
	})
}

func (c *TelegramChannel) isAllowed(userID int64, username string) bool {
	if c.allowlistChecker == nil {
		return true
	}
	return c.allowlistChecker.IsAllowed(userID, username)
}

func (c *TelegramChannel) sendPlainText(ctx context.Context, chatID int64, text string) {
	_, _ = c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
}

// sendOutboundBestEffort sends an OutboundMessage; logs at debug level on failure.
func sendOutboundBestEffort(ctx context.Context, bot *telego.Bot, chatID int64, msg domain.OutboundMessage) {
	if err := sendOutbound(ctx, bot, chatID, msg); err != nil {
		slog.Debug("send outbound failed (best effort)", "chat_id", chatID, "error", err)
	}
}

// sendOutbound sends an OutboundMessage to a Telegram chat.
func sendOutbound(ctx context.Context, bot *telego.Bot, chatID int64, msg domain.OutboundMessage) error {
	for _, img := range msg.Images {
		file := tu.FileFromBytes(img.Data, img.Name)
		if _, err := bot.SendPhoto(ctx, &telego.SendPhotoParams{
			ChatID: tu.ID(chatID),
			Photo:  file,
		}); err != nil {
			slog.Error("failed to send photo", "chat_id", chatID, "error", err)
		}
	}
	for _, f := range msg.Files {
		file := tu.FileFromBytes(f.Data, f.Name)
		if _, err := bot.SendDocument(ctx, &telego.SendDocumentParams{
			ChatID:   tu.ID(chatID),
			Document: file,
		}); err != nil {
			slog.Error("failed to send document", "chat_id", chatID, "error", err)
		}
	}
	if msg.Text != "" {
		chunks := RenderMarkdown(msg.Text)
		for _, chunk := range chunks {
			params := tu.Message(tu.ID(chatID), chunk.Text).WithParseMode(telego.ModeMarkdownV2)
			if _, err := bot.SendMessage(ctx, params); err != nil {
				_, _ = bot.SendMessage(ctx, tu.Message(tu.ID(chatID), msg.Text))
				break
			}
		}
	}
	return nil
}
