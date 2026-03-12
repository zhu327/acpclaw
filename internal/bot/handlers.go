package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/acp"
	"github.com/zhu327/acpclaw/internal/util"
)

func getChatIDFromQuery(query telego.CallbackQuery) int64 {
	if query.Message != nil {
		if id := query.Message.GetChat().ID; id != 0 {
			return id
		}
	}
	return query.From.ID
}

func resolveWorkspace(args []string, defaultWorkspace string) string {
	ws := strings.TrimSpace(strings.Join(args, " "))
	if ws == "" {
		return defaultWorkspace
	}
	return ws
}

func sessionDisplayName(s acp.SessionInfo) string {
	if s.Title != "" {
		return s.Title
	}
	if s.Workspace != "" {
		return s.Workspace
	}
	return s.SessionID
}

func filterNonActiveSessions(sessions []acp.SessionInfo, activeID string) []acp.SessionInfo {
	if activeID == "" {
		return sessions
	}
	out := make([]acp.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if s.SessionID != activeID {
			out = append(out, s)
		}
	}
	return out
}

func parseResumeArgs(args []string) (index *int, valid bool) {
	if len(args) == 0 {
		return nil, true
	}
	if len(args) > 1 {
		return nil, false
	}
	arg := strings.TrimSpace(args[0])
	if arg == "" {
		return nil, false
	}
	if n, err := strconv.Atoi(arg); err == nil {
		return &n, true
	}
	return nil, false
}

const (
	helpText = `👋 ACP-Claw Bot

📋 Session Management
/new [workspace]  — Start a new session
/session  — List all sessions
/resume [N]  — Resume a session

⚙️ Controls
/cancel  — Cancel current prompt
/reconnect  — Reconnect ACP process

ℹ️ /start  — Start the bot
ℹ️ /help  — Show this help`
)

func registerCommandHandlers(b *Bridge) {
	b.handler.HandleMessage(b.handleCommandMessage, th.AnyCommand())
}

func (b *Bridge) handleCommandMessage(ctx *th.Context, msg telego.Message) error {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil
	}
	cmd, _, args := tu.ParseCommand(text)
	if cmd == "" {
		return nil
	}
	return b.handleCommand(ctx, msg, cmd, args)
}

func (b *Bridge) handleCommand(ctx *th.Context, msg telego.Message, cmd string, args []string) error {
	chatID := msg.Chat.ID
	if msg.From == nil {
		return nil
	}
	if !b.IsAllowed(msg.From.ID, msg.From.Username) {
		b.sendText(ctx.Context(), chatID, accessDeniedText)
		return nil
	}

	switch cmd {
	case "start":
		b.sendText(ctx.Context(), chatID, "👋 Welcome! Use /help for available commands.")
	case "help":
		b.sendText(ctx.Context(), chatID, helpText)
	case "new":
		workspace := resolveWorkspace(args, b.cfg.DefaultWorkspace)
		if err := b.agentSvc.NewSession(ctx.Context(), chatID, workspace); err != nil {
			b.sendUserError(ctx.Context(), chatID, "Failed to start session.", err)
			return nil
		}
		info := b.agentSvc.ActiveSession(chatID)
		replyText := "Session started."
		if info != nil {
			replyText = fmt.Sprintf(
				"Session started: `%s` in `%s`",
				escapeMarkdownV2(info.SessionID),
				escapeMarkdownV2(info.Workspace),
			)
		}
		b.sendTextFormatted(ctx.Context(), chatID, replyText)
	case "session":
		sessions, err := b.agentSvc.ListSessions(ctx.Context(), chatID)
		if err != nil {
			if errors.Is(err, acp.ErrNoActiveProcess) {
				b.sendText(ctx.Context(), chatID, "No active session. Use /new first.")
				return nil
			}
			b.sendUserError(ctx.Context(), chatID, "Failed to list sessions.", err)
			return nil
		}
		if len(sessions) == 0 {
			b.sendText(ctx.Context(), chatID, "No sessions found.")
			return nil
		}
		activeID := ""
		if active := b.agentSvc.ActiveSession(chatID); active != nil {
			activeID = active.SessionID
		}
		var lines []string
		for i, s := range sessions {
			marker := ""
			if s.SessionID == activeID {
				marker = " \\(active\\)"
			}
			lines = append(lines, fmt.Sprintf("%d\\. `%s` \\[`%s`\\]%s",
				i+1, escapeMarkdownV2(sessionDisplayName(s)), escapeMarkdownV2(s.SessionID), marker))
		}
		b.sendTextFormatted(ctx.Context(), chatID, strings.Join(lines, "\n"))
	case "resume":
		resumeIdx, valid := parseResumeArgs(args)
		if !valid {
			b.sendText(ctx.Context(), chatID, "Usage: /resume or /resume N")
			return nil
		}
		sessions, err := b.agentSvc.ListSessions(ctx.Context(), chatID)
		if err != nil {
			if errors.Is(err, acp.ErrNoActiveProcess) {
				b.sendText(ctx.Context(), chatID, "No active session. Use /new first.")
				return nil
			}
			b.sendUserError(ctx.Context(), chatID, "Failed to list sessions.", err)
			return nil
		}
		activeID := ""
		if active := b.agentSvc.ActiveSession(chatID); active != nil {
			activeID = active.SessionID
		}
		sessions = filterNonActiveSessions(sessions, activeID)
		if len(sessions) == 0 {
			b.sendText(ctx.Context(), chatID, noResumableSessionsText)
			return nil
		}
		if resumeIdx != nil {
			n := *resumeIdx
			if n < 1 || n > len(sessions) {
				b.sendText(ctx.Context(), chatID, "Invalid session number.")
				return nil
			}
			s := sessions[n-1]
			if err := b.agentSvc.LoadSession(ctx.Context(), chatID, s.SessionID, s.Workspace); err != nil {
				if errors.Is(err, acp.ErrLoadSessionNotSupported) {
					b.sendText(ctx.Context(), chatID, sessionResumeNotSupportedText)
					return nil
				}
				if errors.Is(err, acp.ErrSessionNotFound) {
					b.sendText(ctx.Context(), chatID, sessionExpiredText)
					return nil
				}
				b.sendUserError(ctx.Context(), chatID, "Failed to resume session.", err)
				return nil
			}
			b.sendTextFormatted(
				ctx.Context(),
				chatID,
				fmt.Sprintf(
					"Session resumed: `%s` in `%s`",
					escapeMarkdownV2(s.SessionID),
					escapeMarkdownV2(s.Workspace),
				),
			)
		} else {
			b.resumeChoicesMu.Lock()
			b.pendingResumeChoices[chatID] = sessions
			b.resumeChoicesMu.Unlock()

			keyboard := buildResumeKeyboard(sessions)
			if b.bot != nil {
				params := tu.Message(tu.ID(chatID), "Pick a session to resume:").WithReplyMarkup(keyboard)
				if _, err := b.bot.SendMessage(ctx.Context(), params); err != nil {
					slog.Error("failed to send resume keyboard", "chat_id", chatID, "error", err)
				}
			}
		}
	case "cancel":
		if err := b.agentSvc.Cancel(ctx.Context(), chatID); err != nil {
			if errors.Is(err, acp.ErrNoActiveSession) {
				b.sendText(ctx.Context(), chatID, "No active session. Use /new first.")
				return nil
			}
			b.sendUserError(ctx.Context(), chatID, "Failed to cancel current task.", err)
			return nil
		}
		b.sendText(ctx.Context(), chatID, "Cancelled current operation.")
	case "reconnect":
		workspace := resolveWorkspace(args, b.cfg.DefaultWorkspace)
		if err := b.agentSvc.Reconnect(ctx.Context(), chatID, workspace); err != nil {
			b.sendUserError(ctx.Context(), chatID, "Failed to reconnect.", err)
			return nil
		}
		info := b.agentSvc.ActiveSession(chatID)
		ws := workspace
		sid := ""
		if info != nil {
			ws = info.Workspace
			sid = info.SessionID
		}
		if sid != "" {
			b.sendTextFormatted(ctx.Context(), chatID,
				fmt.Sprintf("🔄 ACP process reconnected\\. New session started: `%s` in `%s`\\.",
					escapeMarkdownV2(sid), escapeMarkdownV2(ws)))
		} else {
			b.sendTextFormatted(ctx.Context(), chatID,
				fmt.Sprintf("🔄 ACP process reconnected\\. New session started in `%s`\\.", escapeMarkdownV2(ws)))
		}
	default:
		b.sendText(ctx.Context(), chatID, "Unknown command. Use /help.")
	}
	return nil
}

func registerCallbackHandlers(b *Bridge) {
	b.handler.HandleCallbackQuery(b.handlePermissionCallback, th.CallbackDataPrefix("perm|"))
	b.handler.HandleCallbackQuery(b.handleBusyCallback, th.CallbackDataPrefix("busy|"))
	b.handler.HandleCallbackQuery(b.handleResumeCallback, th.CallbackDataPrefix("resume|"))
}

func permissionDecisionLabel(d acp.PermissionDecision) string {
	labels := map[acp.PermissionDecision]string{
		acp.PermissionAlways:   "Approved for this session.",
		acp.PermissionThisTime: "Approved this time.",
		acp.PermissionDeny:     "Denied.",
	}
	return labels[d]
}

func formatPermissionDecisionEdit(label string) string {
	return "\nDecision: " + label
}

func decisionInAvailable(available []acp.PermissionDecision, d acp.PermissionDecision) bool {
	for _, a := range available {
		if a == d {
			return true
		}
	}
	return false
}

func (b *Bridge) answerCallback(ctx context.Context, query telego.CallbackQuery, text string) {
	if b.bot != nil {
		_ = b.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID).WithText(text))
	}
}

func (b *Bridge) answerCallbackWithWarn(ctx context.Context, query telego.CallbackQuery, text string) {
	if b.bot != nil {
		if err := b.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID).WithText(text)); err != nil {
			slog.Warn("AnswerCallbackQuery failed", "callback_id", query.ID, "error", err)
		}
	}
}

func (b *Bridge) handlePermissionCallback(ctx *th.Context, query telego.CallbackQuery) error {
	data := query.Data
	if !strings.HasPrefix(data, "perm|") {
		return nil
	}
	parts := strings.SplitN(data, "|", 3)
	if len(parts) != 3 {
		return nil
	}
	reqID := parts[1]
	decisionStr := parts[2]

	if query.From.ID == 0 {
		return nil
	}
	if !b.IsAllowed(query.From.ID, query.From.Username) {
		b.answerCallbackWithWarn(ctx.Context(), query, accessDeniedCallbackText)
		return nil
	}

	var decision acp.PermissionDecision
	switch decisionStr {
	case "always":
		decision = acp.PermissionAlways
	case "once":
		decision = acp.PermissionThisTime
	case "deny":
		decision = acp.PermissionDeny
	default:
		b.answerCallbackWithWarn(ctx.Context(), query, permRequestExpiredText)
		return nil
	}

	b.permMu.Lock()
	available := b.pendingPermActions[reqID]
	b.permMu.Unlock()
	if !decisionInAvailable(available, decision) {
		b.answerCallbackWithWarn(ctx.Context(), query, permRequestExpiredText)
		if b.onPermCallbackAnswer != nil {
			b.onPermCallbackAnswer(permRequestExpiredText)
		}
		return nil
	}

	b.RespondPermission(reqID, decision)
	chatID := getChatIDFromQuery(query)

	if decision == acp.PermissionAlways {
		b.agentSvc.SetSessionPermissionMode(chatID, acp.PermissionModeApprove)
	}

	label := permissionDecisionLabel(decision)

	if b.bot != nil {
		params := tu.CallbackQuery(query.ID).WithText(label)
		if err := b.bot.AnswerCallbackQuery(ctx.Context(), params); err != nil {
			slog.Warn("AnswerCallbackQuery failed", "callback_id", query.ID, "error", err)
		}

		msgID := 0
		if query.Message != nil {
			msgID = query.Message.GetMessageID()
		}

		originalText := ""
		if m, ok := query.Message.(*telego.Message); ok && m != nil {
			originalText = m.Text
		}
		edited := originalText + formatPermissionDecisionEdit(label)
		_, editErr := b.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
			ChatID:    tu.ID(chatID),
			MessageID: msgID,
			Text:      edited,
		})
		if editErr != nil {
			slog.Warn("EditMessageText failed", "chat_id", chatID, "message_id", msgID, "error", editErr)
			_, _ = b.bot.EditMessageReplyMarkup(ctx.Context(), &telego.EditMessageReplyMarkupParams{
				ChatID:      tu.ID(chatID),
				MessageID:   msgID,
				ReplyMarkup: tu.InlineKeyboard(),
			})
		}
	}
	return nil
}

func (b *Bridge) handleResumeCallback(ctx *th.Context, query telego.CallbackQuery) error {
	data := query.Data
	if !strings.HasPrefix(data, "resume|") {
		return nil
	}
	if query.From.ID == 0 || !b.IsAllowed(query.From.ID, query.From.Username) {
		b.answerCallback(ctx.Context(), query, accessDeniedCallbackText)
		return nil
	}
	indexStr := strings.TrimPrefix(data, "resume|")
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		b.answerCallback(ctx.Context(), query, invalidSelectionText)
		if b.onResumeCallbackAnswer != nil {
			b.onResumeCallbackAnswer(invalidSelectionText)
		}
		return nil
	}

	chatID := getChatIDFromQuery(query)
	slog.Info("resume callback received", "chat_id", chatID, "index", index)

	b.resumeChoicesMu.Lock()
	candidates := b.pendingResumeChoices[chatID]
	delete(b.pendingResumeChoices, chatID)
	b.resumeChoicesMu.Unlock()

	if candidates == nil {
		slog.Warn("resume callback: no candidates", "chat_id", chatID, "index", index)
		b.answerCallback(ctx.Context(), query, selectionExpiredText)
		return nil
	}
	if index < 0 || index >= len(candidates) {
		slog.Warn(
			"resume callback: invalid index",
			"chat_id",
			chatID,
			"index",
			index,
			"candidates_len",
			len(candidates),
		)
		b.answerCallback(ctx.Context(), query, invalidSelectionText)
		return nil
	}

	s := candidates[index]
	if err := b.agentSvc.LoadSession(ctx.Context(), chatID, s.SessionID, s.Workspace); err != nil {
		if errors.Is(err, acp.ErrLoadSessionNotSupported) {
			b.answerCallback(ctx.Context(), query, sessionResumeNotSupportedText)
			b.sendText(ctx.Context(), chatID, sessionResumeNotSupportedText)
			return nil
		}
		if errors.Is(err, acp.ErrSessionNotFound) {
			b.answerCallback(ctx.Context(), query, sessionExpiredText)
			b.sendText(ctx.Context(), chatID, sessionExpiredText)
			return nil
		}
		b.answerCallback(ctx.Context(), query, "Failed to resume.")
		b.sendUserError(ctx.Context(), chatID, "Failed to resume session.", err)
		return nil
	}

	b.answerCallback(ctx.Context(), query, "Session resumed.")
	if b.bot != nil && query.Message != nil {
		msgID := query.Message.GetMessageID()
		_, _ = b.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
			ChatID:    tu.ID(chatID),
			MessageID: msgID,
			Text:      fmt.Sprintf("Resumed session: %s\nWorkspace: %s", s.SessionID, s.Workspace),
		})
	}
	b.sendTextFormatted(
		ctx.Context(),
		chatID,
		fmt.Sprintf("Session resumed: `%s` in `%s`", escapeMarkdownV2(s.SessionID), escapeMarkdownV2(s.Workspace)),
	)
	return nil
}

func isCommandToSkip(msg *telego.Message) bool {
	if msg == nil {
		return false
	}
	text := extractTextFromMessage(msg)
	if text == "" {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

func extractTextFromMessage(msg *telego.Message) string {
	if msg == nil {
		return ""
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	return text
}

func processNonImageDocument(docData []byte, mimeType, fileName string) acp.FileData {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if fileName == "" {
		fileName = "attachment.bin"
	}
	fd := acp.FileData{MIMEType: mimeType, Data: docData, Name: fileName}
	if utf8.Valid(docData) {
		s := string(docData)
		fd.TextContent = &s
	}
	return fd
}

func (b *Bridge) handleUserMessage(ctx *th.Context, msg telego.Message) error {
	if isCommandToSkip(&msg) {
		return nil
	}

	chatID := msg.Chat.ID
	if msg.From == nil {
		return nil
	}
	if !b.IsAllowed(msg.From.ID, msg.From.Username) {
		b.sendText(ctx.Context(), chatID, accessDeniedText)
		return nil
	}

	text := extractTextFromMessage(&msg)

	var images []acp.ImageData
	var files []acp.FileData

	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		imgData, err := b.downloadFile(ctx.Context(), photo.FileID)
		if err != nil {
			slog.Error("failed to download photo", "chat_id", chatID, "error", err)
		} else {
			images = append(images, acp.ImageData{MIMEType: "image/jpeg", Data: imgData})
		}
	}

	if msg.Document != nil {
		docData, err := b.downloadFile(ctx.Context(), msg.Document.FileID)
		if err != nil {
			slog.Error("failed to download document", "chat_id", chatID, "error", err)
		} else {
			mime := msg.Document.MimeType
			if strings.HasPrefix(mime, "image/") {
				images = append(images, acp.ImageData{MIMEType: mime, Data: docData, Name: msg.Document.FileName})
			} else {
				files = append(files, processNonImageDocument(docData, mime, msg.Document.FileName))
			}
		}
	}

	if text == "" && len(images) == 0 && len(files) == 0 {
		return nil
	}

	if active := b.agentSvc.ActiveSession(chatID); active == nil {
		startLock := b.implicitStartMutex(chatID)
		startLock.Lock()
		defer startLock.Unlock()
		if active := b.agentSvc.ActiveSession(chatID); active == nil {
			workspace := b.cfg.DefaultWorkspace
			if workspace == "" {
				workspace = "."
			}
			if err := b.agentSvc.NewSession(ctx.Context(), chatID, workspace); err != nil {
				b.sendUserError(ctx.Context(), chatID, "Failed to start session.", err)
				return nil
			}
		}
	}

	input := acp.PromptInput{Text: text, Images: images, Files: files}
	slog.Info("Prompt received", "chat_id", chatID, "text", util.LogTextPreview(text, 200))

	lock := b.chatMutex(chatID)
	if !lock.TryLock() {
		b.queueBusyPrompt(ctx.Context(), chatID, input, msg.MessageID)
		return nil
	}
	defer lock.Unlock()

	b.runPromptLoop(ctx.Context(), chatID, input)
	return nil
}

func (b *Bridge) runPromptLoop(ctx context.Context, chatID int64, input acp.PromptInput) {
	for {
		reply, err := b.agentSvc.Prompt(ctx, chatID, input)
		if err != nil {
			if _, ok := b.cancelRequested.LoadAndDelete(chatID); !ok {
				if errors.Is(err, acp.ErrAgentOutputLimitExceeded) {
					b.sendText(ctx, chatID, stdioLimitExceededText)
				} else if errors.Is(err, acp.ErrNoActiveSession) {
					b.sendText(ctx, chatID, noActiveSessionPromptText)
				} else {
					b.sendUserError(ctx, chatID, "Failed to process your request.", err)
				}
			}
			if reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0) {
				b.sendReply(ctx, chatID, reply)
			}
		} else if reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0) {
			b.sendReply(ctx, chatID, reply)
		}

		p := b.popPending(chatID)
		if p == nil {
			return
		}
		b.clearBusyNotification(ctx, p)
		input = p.input
	}
}

func (b *Bridge) handleBusyCallback(ctx *th.Context, query telego.CallbackQuery) error {
	data := query.Data
	if !strings.HasPrefix(data, "busy|") {
		return nil
	}
	if !b.IsAllowed(query.From.ID, query.From.Username) {
		b.answerCallback(ctx.Context(), query, accessDeniedCallbackText)
		if b.onBusyAccessDenied != nil {
			b.onBusyAccessDenied(accessDeniedCallbackText)
		}
		return nil
	}
	token := strings.TrimPrefix(data, "busy|")
	chatID := getChatIDFromQuery(query)

	b.pendingMu.Lock()
	p := b.pendingByChat[chatID]
	if p == nil || p.token != token {
		b.pendingMu.Unlock()
		b.answerCallback(ctx.Context(), query, busyAlreadySentText)
		if b.bot != nil && query.Message != nil {
			_, _ = b.bot.EditMessageReplyMarkup(ctx.Context(), &telego.EditMessageReplyMarkupParams{
				ChatID:      tu.ID(chatID),
				MessageID:   query.Message.GetMessageID(),
				ReplyMarkup: tu.InlineKeyboard(),
			})
		}
		if b.onBusyStale != nil {
			b.onBusyStale(busyAlreadySentText, true)
		}
		return nil
	}
	b.pendingMu.Unlock()
	b.cancelRequested.Store(chatID, struct{}{})

	if err := b.agentSvc.Cancel(ctx.Context(), chatID); err != nil {
		b.answerCallback(ctx.Context(), query, busyCancelFailedText)
		if b.bot != nil && query.Message != nil {
			_, _ = b.bot.EditMessageReplyMarkup(ctx.Context(), &telego.EditMessageReplyMarkupParams{
				ChatID:      tu.ID(chatID),
				MessageID:   query.Message.GetMessageID(),
				ReplyMarkup: tu.InlineKeyboard(),
			})
		}
		if b.onBusyCancelFailure != nil {
			b.onBusyCancelFailure(busyCancelFailedText)
		}
		b.cancelRequested.Delete(chatID)
		return nil
	}

	b.pendingMu.Lock()
	msgID := p.notifyMsgID
	p.notifyMsgID = 0
	b.pendingMu.Unlock()
	if query.Message != nil {
		msgID = query.Message.GetMessageID()
	}
	b.answerCallback(ctx.Context(), query, busySentText)
	if b.bot != nil {
		if msgID != 0 {
			_, _ = b.bot.EditMessageText(ctx.Context(), &telego.EditMessageTextParams{
				ChatID:    tu.ID(chatID),
				MessageID: msgID,
				Text:      busySentText,
			})
			_, _ = b.bot.EditMessageReplyMarkup(ctx.Context(), &telego.EditMessageReplyMarkupParams{
				ChatID:      tu.ID(chatID),
				MessageID:   msgID,
				ReplyMarkup: tu.InlineKeyboard(),
			})
		}
	}
	if b.onBusyMatchingTokenDone != nil {
		b.onBusyMatchingTokenDone()
	}
	return nil
}

func (b *Bridge) sendUserError(ctx context.Context, chatID int64, userMessage string, err error) {
	if err != nil {
		slog.Error("user-visible error", "chat_id", chatID, "message", userMessage, "error", err)
	}
	b.sendText(ctx, chatID, "❌ "+userMessage)
}
