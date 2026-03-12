package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/acp"
	"github.com/zhu327/acpclaw/internal/util"
)

// Parity constants: user-visible strings must match Python exactly.
const (
	accessDeniedText              = "Access denied for this bot."
	accessDeniedCallbackText      = "Access denied."   // Callback answer when user not in allowlist (Python parity)
	permRequestExpiredText        = "Request expired." // Callback answer when action not in available_actions (Python parity)
	stdioLimitExceededText        = "Agent output exceeded ACP stdio limit. Restart with a higher `--acp-stdio-limit` (or `ACP_STDIO_LIMIT`)."
	noActiveSessionPromptText     = "No active session. Send a message again or use /new [workspace]."
	noResumableSessionsText       = "No resumable sessions found."
	selectionExpiredText          = "Selection expired." // Resume callback when candidates nil (Python parity)
	invalidSelectionText          = "Invalid selection." // Resume callback when index out of range (Python parity)
	busySendNowButtonText         = "Send now"           // Queue message button; no extra emoji (Python parity)
	busySentText                  = "✅ Sent."            // Shown when "Send now" succeeds (callback path only)
	busyAlreadySentText           = "Already sent."
	busyCancelFailedText          = "Cancel failed."
	sessionResumeNotSupportedText = "Session resume is not supported by the current agent."
	sessionExpiredText            = "Session expired or no longer available."
)

func buildPermCallbackData(reqID, action string) string {
	return fmt.Sprintf("perm|%s|%s", reqID, action)
}

func buildBusyCallbackData(token string) string {
	return fmt.Sprintf("busy|%s", token)
}

type pendingPrompt struct {
	input        acp.PromptInput
	chatID       int64
	token        string
	notifyMsgID  int
	replyToMsgID int
}

type Config struct {
	AllowedUserIDs   []int64
	AllowedUsernames []string
	DefaultWorkspace string
}

type Bridge struct {
	bot                     *telego.Bot
	handler                 *th.BotHandler
	agentSvc                acp.AgentService
	cfg                     Config
	ctx                     context.Context
	cancel                  context.CancelFunc
	pendingPerms            map[string]chan acp.PermissionResponse
	permMu                  sync.Mutex
	pendingByChat           map[int64]*pendingPrompt
	pendingMu               sync.Mutex
	chatLocks               sync.Map
	implicitStartLocks      sync.Map
	cancelRequested         sync.Map
	pendingResumeChoices    map[int64][]acp.SessionInfo
	resumeChoicesMu         sync.Mutex
	onBusyAccessDenied      func(answer string)
	onBusyStale             func(answer string, clearMarkup bool)
	onBusyCancelFailure     func(answer string)
	onBusyMatchingTokenDone func()
	onClearBusyNotification func(clearMarkupOnly bool)
	onPermCallbackAnswer    func(answer string)
	onResumeCallbackAnswer  func(answer string)
	pendingPermActions      map[string][]acp.PermissionDecision
}

const permissionRequestTTL = 5 * time.Minute

func NewBridge(bot *telego.Bot, agentSvc acp.AgentService, cfg Config) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{
		bot:                  bot,
		agentSvc:             agentSvc,
		cfg:                  cfg,
		ctx:                  ctx,
		cancel:               cancel,
		pendingPerms:         make(map[string]chan acp.PermissionResponse),
		pendingPermActions:   make(map[string][]acp.PermissionDecision),
		pendingByChat:        make(map[int64]*pendingPrompt),
		pendingResumeChoices: make(map[int64][]acp.SessionInfo),
	}
	return b
}

func (b *Bridge) chatMutex(chatID int64) *sync.Mutex {
	v, _ := b.chatLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (b *Bridge) implicitStartMutex(chatID int64) *sync.Mutex {
	v, _ := b.implicitStartLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func randomToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (b *Bridge) queueBusyPrompt(ctx context.Context, chatID int64, input acp.PromptInput, replyToMsgID int) {
	token := randomToken()

	b.pendingMu.Lock()
	old := b.pendingByChat[chatID]
	b.pendingByChat[chatID] = &pendingPrompt{
		input:        input,
		chatID:       chatID,
		token:        token,
		replyToMsgID: replyToMsgID,
	}
	b.pendingMu.Unlock()

	b.clearOldBusyNotification(ctx, chatID, old)
	b.sendBusyNotification(ctx, chatID, token, replyToMsgID)
}

func (b *Bridge) clearOldBusyNotification(ctx context.Context, chatID int64, old *pendingPrompt) {
	if old == nil || old.notifyMsgID == 0 || b.bot == nil {
		return
	}
	_, _ = b.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(chatID),
		MessageID:   old.notifyMsgID,
		ReplyMarkup: tu.InlineKeyboard(),
	})
}

func (b *Bridge) sendBusyNotification(ctx context.Context, chatID int64, token string, replyToMsgID int) {
	if b.bot == nil {
		return
	}
	keyboard := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(busySendNowButtonText).WithCallbackData(buildBusyCallbackData(token)),
		),
	)
	params := tu.Message(tu.ID(chatID), "⏳ Agent is busy. Your message is queued.").WithReplyMarkup(keyboard)
	if replyToMsgID > 0 {
		params.ReplyParameters = &telego.ReplyParameters{MessageID: replyToMsgID}
	}
	sent, err := b.bot.SendMessage(ctx, params)
	if err != nil {
		return
	}
	b.pendingMu.Lock()
	if p := b.pendingByChat[chatID]; p != nil && p.token == token {
		p.notifyMsgID = sent.MessageID
	}
	b.pendingMu.Unlock()
}

func (b *Bridge) popPending(chatID int64) *pendingPrompt {
	b.pendingMu.Lock()
	p := b.pendingByChat[chatID]
	delete(b.pendingByChat, chatID)
	b.pendingMu.Unlock()
	return p
}

func (b *Bridge) clearBusyNotification(ctx context.Context, p *pendingPrompt) {
	if b.onClearBusyNotification != nil && p != nil {
		b.onClearBusyNotification(true)
	}
	if p == nil || b.bot == nil {
		return
	}
	b.pendingMu.Lock()
	notifyMsgID := p.notifyMsgID
	b.pendingMu.Unlock()
	if notifyMsgID == 0 {
		return
	}
	_, _ = b.bot.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(p.chatID),
		MessageID:   notifyMsgID,
		ReplyMarkup: tu.InlineKeyboard(),
	})
}

func (b *Bridge) IsAllowed(userID int64, username string) bool {
	if len(b.cfg.AllowedUserIDs) == 0 && len(b.cfg.AllowedUsernames) == 0 {
		return true
	}
	for _, id := range b.cfg.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	usernameLower := strings.ToLower(strings.TrimSpace(username))
	for _, u := range b.cfg.AllowedUsernames {
		if strings.ToLower(strings.TrimSpace(u)) == usernameLower {
			return true
		}
	}
	return false
}

func (b *Bridge) RegisterHandlers(updates <-chan telego.Update) error {
	if b.bot == nil {
		return nil
	}
	var err error
	b.handler, err = th.NewBotHandler(b.bot, updates)
	if err != nil {
		return fmt.Errorf("create bot handler: %w", err)
	}
	b.setupPermissionHandler()
	b.setupActivityHandler()
	registerCommandHandlers(b)
	registerCallbackHandlers(b)
	b.handler.HandleMessage(b.handleUserMessage, th.AnyMessage())
	return nil
}

func (b *Bridge) Run(ctx context.Context) error {
	if b.handler == nil {
		return fmt.Errorf("handler not initialized: call RegisterHandlers first")
	}
	if err := b.handler.Start(); err != nil {
		return err
	}
	<-ctx.Done()
	b.cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	return b.handler.StopWithContext(shutdownCtx)
}

func (b *Bridge) RespondPermission(reqID string, decision acp.PermissionDecision) {
	b.permMu.Lock()
	ch, ok := b.pendingPerms[reqID]
	delete(b.pendingPerms, reqID)
	delete(b.pendingPermActions, reqID)
	b.permMu.Unlock()
	if ok && ch != nil {
		select {
		case ch <- acp.PermissionResponse{Decision: decision}:
		default:
		}
	}
}

func defaultPermissionActions(actions []acp.PermissionDecision) []acp.PermissionDecision {
	if len(actions) == 0 {
		return []acp.PermissionDecision{acp.PermissionDeny}
	}
	return actions
}

func (b *Bridge) setupPermissionHandler() {
	b.agentSvc.SetPermissionHandler(func(chatID int64, req acp.PermissionRequest) <-chan acp.PermissionResponse {
		ch := make(chan acp.PermissionResponse, 1)
		actions := defaultPermissionActions(req.AvailableActions)
		b.permMu.Lock()
		b.pendingPerms[req.ID] = ch
		b.pendingPermActions[req.ID] = actions
		b.permMu.Unlock()
		go b.expirePermissionRequest(req.ID, ch)

		if b.bot != nil {
			b.sendPermissionRequest(chatID, req, formatPermissionRequest(req.Tool))
		}
		return ch
	})
}

func buildPermissionKeyboard(reqID string, actions []acp.PermissionDecision) *telego.InlineKeyboardMarkup {
	var row []telego.InlineKeyboardButton
	labels := map[acp.PermissionDecision]string{
		acp.PermissionAlways:   "Always",
		acp.PermissionThisTime: "This time",
		acp.PermissionDeny:     "Deny",
	}
	callbackActions := map[acp.PermissionDecision]string{
		acp.PermissionAlways:   "always",
		acp.PermissionThisTime: "once",
		acp.PermissionDeny:     "deny",
	}
	for _, a := range actions {
		if label, ok := labels[a]; ok {
			btn := tu.InlineKeyboardButton(label).WithCallbackData(buildPermCallbackData(reqID, callbackActions[a]))
			row = append(row, btn)
		}
	}
	return tu.InlineKeyboard(tu.InlineKeyboardRow(row...))
}

func (b *Bridge) sendPermissionRequest(chatID int64, req acp.PermissionRequest, text string) {
	actions := defaultPermissionActions(req.AvailableActions)
	keyboard := buildPermissionKeyboard(req.ID, actions)
	chunks := RenderMarkdown(text)
	if len(chunks) == 0 {
		_, _ = b.bot.SendMessage(b.ctx, tu.Message(tu.ID(chatID), text).WithReplyMarkup(keyboard))
		return
	}
	params := &telego.SendMessageParams{
		ChatID:      tu.ID(chatID),
		Text:        chunks[0].Text,
		ParseMode:   telego.ModeMarkdownV2,
		ReplyMarkup: keyboard,
	}
	if _, err := b.bot.SendMessage(b.ctx, params); err != nil {
		if _, err2 := b.bot.SendMessage(b.ctx, tu.Message(tu.ID(chatID), text).WithReplyMarkup(keyboard)); err2 != nil {
			slog.Error("failed to send permission request", "chat_id", chatID, "error", err2)
		}
	}
	for _, chunk := range chunks[1:] {
		b.sendMarkdownChunks(b.ctx, chatID, []MessageChunk{chunk})
	}
}

func (b *Bridge) expirePermissionRequest(reqID string, ch chan acp.PermissionResponse) {
	timer := time.NewTimer(permissionRequestTTL)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-b.ctx.Done():
		return
	}

	b.permMu.Lock()
	current, ok := b.pendingPerms[reqID]
	if ok && current == ch {
		delete(b.pendingPerms, reqID)
		delete(b.pendingPermActions, reqID)
	}
	b.permMu.Unlock()
	if ok && current == ch {
		select {
		case ch <- acp.PermissionResponse{Decision: acp.PermissionDeny}:
		default:
		}
	}
}

var searchLocalWordBoundary = regexp.MustCompile(
	`\b(workspace|repository|repo|project|ripgrep|rg|grep|glob)\b`,
)

func searchSourceLabel(title, text string) string {
	content := strings.ToLower(title + "\n" + text)
	if isWebSearch(content) {
		return "🌐 Searching web"
	}
	if strings.Contains(content, "file://") || searchLocalWordBoundary.MatchString(content) {
		return "🔎 Querying project"
	}
	return ""
}

func isWebSearch(content string) bool {
	webHints := []string{"http://", "https://", "url:", "web search", "internet"}
	for _, h := range webHints {
		if strings.Contains(content, h) {
			return true
		}
	}
	return false
}

func fencedCode(text string) string {
	maxRun := 0
	run := 0
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

func formatPermissionRequest(toolTitle string) string {
	parts := []string{"**⚠️ Permission required**"}
	title := strings.TrimSpace(toolTitle)
	if title != "" {
		if runParts, ok := formatRunCommands(title); ok {
			parts = append(parts, runParts...)
		} else {
			parts = append(parts, title)
		}
	}
	return strings.Join(parts, "\n\n")
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

func formatActivityMessage(block acp.ActivityBlock, workspace string) string {
	label := block.Label
	if block.Kind == acp.ActivitySearch {
		if searchLabel := searchSourceLabel(block.Detail, block.Text); searchLabel != "" {
			label = searchLabel
		}
	}
	parts := []string{"**" + label + "**"}

	detail := block.Detail
	switch block.Kind {
	case acp.ActivityExecute:
		if runParts, ok := formatRunCommands(detail); ok {
			parts = append(parts, runParts...)
			detail = ""
		}
	case acp.ActivityRead, acp.ActivityEdit:
		prefix := map[acp.ActivityKind]string{acp.ActivityRead: "Read ", acp.ActivityEdit: "Edit "}[block.Kind]
		if strings.HasPrefix(detail, prefix) {
			path := formatActivityPath(strings.TrimPrefix(detail, prefix), workspace)
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

func (b *Bridge) sendMarkdownChunks(ctx context.Context, chatID int64, chunks []MessageChunk) {
	for _, chunk := range chunks {
		params := tu.Message(tu.ID(chatID), chunk.Text).WithParseMode(telego.ModeMarkdownV2)
		if _, err := b.bot.SendMessage(ctx, params); err != nil {
			_, _ = b.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), chunk.Text))
		}
	}
}

func (b *Bridge) setupActivityHandler() {
	b.agentSvc.SetActivityHandler(func(chatID int64, block acp.ActivityBlock) {
		if b.bot == nil {
			return
		}
		workspace := ""
		if info := b.agentSvc.ActiveSession(chatID); info != nil {
			workspace = info.Workspace
		}
		text := formatActivityMessage(block, workspace)
		chunks := RenderMarkdown(text)
		b.sendMarkdownChunks(b.ctx, chatID, chunks)
	})
}

func buildResumeKeyboard(sessions []acp.SessionInfo) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	for i, s := range sessions {
		if i >= 10 {
			break
		}
		displayName := s.Title
		if displayName == "" && s.Workspace != "" {
			displayName = s.Workspace
		}
		if displayName == "" {
			displayName = s.SessionID
		}
		label := fmt.Sprintf("%d. %s", i+1, displayName)
		if len(label) > 48 {
			label = label[:48]
		}
		rows = append(rows, tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(label).WithCallbackData(fmt.Sprintf("resume|%d", i)),
		))
	}
	return tu.InlineKeyboard(rows...)
}

func (b *Bridge) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	if b.bot == nil {
		return nil, fmt.Errorf("bot not initialized")
	}
	file, err := b.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	url := b.bot.FileDownloadURL(file.FilePath)
	return tu.DownloadFile(url)
}

func (b *Bridge) sendText(ctx context.Context, chatID int64, text string) {
	b.sendTextWithFormat(ctx, chatID, text, false)
}

func (b *Bridge) sendTextFormatted(ctx context.Context, chatID int64, text string) {
	b.sendTextWithFormat(ctx, chatID, text, true)
}

func (b *Bridge) sendTextWithFormat(ctx context.Context, chatID int64, text string, preFormatted bool) {
	if b.bot == nil {
		return
	}
	toSend := text
	if !preFormatted {
		toSend = escapeMarkdownV2(text)
	}
	params := tu.Message(tu.ID(chatID), toSend).WithParseMode(telego.ModeMarkdownV2)
	if _, err := b.bot.SendMessage(ctx, params); err != nil {
		_, _ = b.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
	}
}

func (b *Bridge) sendAttachment(
	ctx context.Context,
	chatID int64,
	data []byte,
	name, defaultName string,
	isImage bool,
) {
	if name == "" {
		name = defaultName
	}
	nr := &util.NamedReader{FileName: name, R: bytes.NewReader(data)}
	file := tu.File(nr)
	var err error
	if isImage {
		_, err = b.bot.SendPhoto(ctx, &telego.SendPhotoParams{ChatID: tu.ID(chatID), Photo: file})
	} else {
		_, err = b.bot.SendDocument(ctx, &telego.SendDocumentParams{ChatID: tu.ID(chatID), Document: file})
	}
	if err != nil {
		slog.Error("failed to send attachment", "chat_id", chatID, "error", err)
	}
}

func (b *Bridge) sendReply(ctx context.Context, chatID int64, reply *acp.AgentReply) {
	if b.bot == nil || reply == nil {
		return
	}
	for _, img := range reply.Images {
		b.sendAttachment(ctx, chatID, img.Data, img.Name, "image", true)
	}
	for _, f := range reply.Files {
		b.sendAttachment(ctx, chatID, f.Data, f.Name, "file", false)
	}
	if reply.Text != "" {
		chunks := RenderMarkdown(reply.Text)
		if len(chunks) > 0 {
			b.sendMarkdownChunks(ctx, chatID, chunks)
		}
	}
}
