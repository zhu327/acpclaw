package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/mymmrac/telego"
	"github.com/zhu327/acpclaw/internal/builtin/agent"
	tgchannel "github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	wxchannel "github.com/zhu327/acpclaw/internal/builtin/channel/weixin"
	"github.com/zhu327/acpclaw/internal/builtin/commands"
	"github.com/zhu327/acpclaw/internal/builtin/memory"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/framework"
	"github.com/zhu327/acpclaw/internal/templates"
	weixinbot "github.com/zhu327/weixin-bot"
	"golang.org/x/net/proxy"
)

const promptQueueFullMessage = "⏳ The prompt queue is full. Try again in a moment or use /cancel to clear pending messages."

// BuiltinPlugin provides the default implementation of all framework hooks.
type BuiltinPlugin struct {
	cfg         *config.Config
	echo        bool
	fw          domain.PluginContext
	sessionMgr  domain.SessionManager
	prompter    domain.Prompter
	permHandler domain.PermissionHandler
	actObserver domain.ActivityObserver
	modelMgr    domain.ModelManager
	modeMgr     domain.ModeManager
	tgChannel   *tgchannel.TelegramChannel
	wxChannel   *wxchannel.WeixinChannel
	resumeStore commands.ResumeChoicesStore
	executor    *promptExecutor
	queue       *promptQueueManager
	memorySvc   *memory.Service
	shutdownFn  func()
}

// NewPlugin creates a new BuiltinPlugin.
func NewPlugin(cfg *config.Config, echoMode bool) (*BuiltinPlugin, error) {
	return &BuiltinPlugin{cfg: cfg, echo: echoMode}, nil
}

// Name implements framework.Plugin.
func (b *BuiltinPlugin) Name() string { return "builtin" }

// Init implements domain.PluginInitializer.
func (b *BuiltinPlugin) Init(fw domain.PluginContext) error {
	b.fw = fw
	b.buildAgentService()
	b.resumeStore = commands.NewResumeChoicesStore()
	b.executor = newPromptExecutor(b.sessionMgr, b.prompter, b.buildFirstPromptPrefix)
	b.queue = newPromptQueueManager(
		b.cfg.Agent.PromptQueue.MaxQueued,
		idleReclaimDuration(b.cfg.Agent.PromptQueue),
		nil,
		context.Background(),
		func(ctx context.Context, j *promptJob) {
			b.completePromptJob(ctx, j)
		},
	)
	b.buildMemoryService()
	b.wireAgentCallbacks()
	return nil
}

func (b *BuiltinPlugin) buildMemoryService() {
	if !b.cfg.Memory.Enabled {
		return
	}
	memoryDir := config.GetAcpclawMemoryDir()
	historyDir := config.GetAcpclawHistoryDir()
	svc, err := memory.NewService(memoryDir, historyDir, templates.FS)
	if err != nil {
		slog.Warn("memory service init failed", "error", err)
		return
	}
	if err := svc.Reindex(); err != nil {
		slog.Warn("memory reindex failed", "error", err)
	}
	b.memorySvc = svc
	slog.Info("memory service enabled", "dir", memoryDir)
}

func (b *BuiltinPlugin) buildFirstPromptPrefix(chat domain.ChatRef) string {
	var parts []string
	if b.cfg.Memory.FirstPromptContext && b.memorySvc != nil {
		if memCtx, err := b.memorySvc.BuildSessionContext(context.Background()); err == nil && memCtx != "" {
			parts = append(parts, memCtx)
		}
	}
	parts = append(parts, buildSessionInfoBlock(chat))
	return strings.Join(parts, "\n\n")
}

// SaveState implements domain.StateSaver.
func (b *BuiltinPlugin) SaveState(ctx context.Context, sessionID string, state domain.State) error {
	if b.memorySvc == nil {
		return nil
	}
	chatID, _ := state["chat_id"].(string)
	if chatID == "" {
		return nil
	}
	b.appendStateToHistory(chatID, state)
	return nil
}

func (b *BuiltinPlugin) appendStateToHistory(chatID string, state domain.State) {
	if userText, ok := state["user_text"].(string); ok && userText != "" {
		if err := b.memorySvc.AppendHistory(chatID, "user", userText); err != nil {
			slog.Warn("failed to append user history", "chat", chatID, "error", err)
		}
	}
	if reply, ok := state["reply"].(*domain.AgentReply); ok && reply != nil && reply.Text != "" {
		if err := b.memorySvc.AppendHistory(chatID, "assistant", reply.Text); err != nil {
			slog.Warn("failed to append assistant history", "chat", chatID, "error", err)
		}
	}
}

func (b *BuiltinPlugin) wireAgentCallbacks() {
	b.permHandler.SetPermissionHandler(b.handlePermissionRequest)
	b.actObserver.SetActivityHandler(b.handleActivityBlock)
}

func (b *BuiltinPlugin) responderForChat(chat domain.ChatRef) domain.Responder {
	if src, ok := b.prompter.(domain.PromptResponderSource); ok {
		if r := src.ActivePromptResponder(chat); r != nil {
			return r
		}
	}
	return b.fw.GetResponder(chat)
}

func (b *BuiltinPlugin) handlePermissionRequest(
	chat domain.ChatRef,
	req domain.PermissionRequest,
) <-chan domain.PermissionResponse {
	ch := b.fw.RegisterPendingPermission(req.ID, chat)
	if resp := b.responderForChat(chat); resp != nil {
		uiReq := domain.ChannelPermissionRequest{
			ID:               req.ID,
			Tool:             req.Tool,
			Description:      req.Description,
			AvailableActions: permDecisionsToStrings(req.AvailableActions),
		}
		if err := resp.ShowPermissionUI(uiReq); err != nil {
			slog.Warn("failed to show permission UI", "chat", chat.CompositeKey(), "error", err)
		}
	}
	return ch
}

func (b *BuiltinPlugin) handleActivityBlock(chat domain.ChatRef, block domain.ActivityBlock) {
	resp := b.responderForChat(chat)
	if resp == nil {
		return
	}
	workspace := ""
	if info := b.sessionMgr.ActiveSession(chat); info != nil {
		workspace = info.Workspace
	}
	block.Workspace = workspace
	if err := resp.SendActivity(block); err != nil {
		slog.Debug("failed to send activity", "chat", chat.CompositeKey(), "error", err)
	}
}

func permDecisionsToStrings(d []domain.PermissionDecision) []string {
	out := make([]string, len(d))
	for i, x := range d {
		out[i] = string(x)
	}
	return out
}

// Channels implements domain.ChannelProvider.
func (b *BuiltinPlugin) Channels() []domain.Channel {
	var channels []domain.Channel
	if b.tgChannel != nil {
		channels = append(channels, b.tgChannel)
	}
	if b.wxChannel != nil {
		channels = append(channels, b.wxChannel)
	}
	return channels
}

func (b *BuiltinPlugin) defaultWorkspace() string {
	if b.cfg.Agent.Workspace != "" {
		return b.cfg.Agent.Workspace
	}
	return "."
}

// Commands implements domain.CommandProvider.
func (b *BuiltinPlugin) Commands() []domain.Command {
	if b.sessionMgr == nil {
		return nil
	}
	beforeSwitch := b.buildBeforeSessionSwitch()
	defaultWs := b.defaultWorkspace()
	return []domain.Command{
		commands.NewStartCommand(),
		commands.NewHelpCommand(),
		commands.NewNewCommand(b.sessionMgr, defaultWs, beforeSwitch),
		commands.NewSessionCommand(b.sessionMgr),
		commands.NewResumeCommand(b.sessionMgr, b.resumeStore),
		commands.NewCancelCommand(b.cancelPromptQueueAndRunning),
		commands.NewReconnectCommand(b.sessionMgr, defaultWs),
		commands.NewStatusCommand(b.sessionMgr),
		commands.NewModelCommand(b.modelMgr),
		commands.NewModeCommand(b.modeMgr),
		commands.NewRestartCommand(b.Shutdown),
	}
}

// StartBackgroundTasks starts background tasks like memory reindex. Call after Init.
func (b *BuiltinPlugin) StartBackgroundTasks(ctx context.Context) {
	if b.memorySvc != nil {
		go b.memorySvc.StartPeriodicReindex(ctx)
	}
}

// Shutdown stops the agent service and closes memory. Call on process exit.
// Agent subprocesses are stopped first so in-flight Prompt calls return before queue workers are joined.
func (b *BuiltinPlugin) Shutdown() {
	if b.shutdownFn != nil {
		b.shutdownFn()
	}
	if b.queue != nil {
		b.queue.Shutdown()
	}
	if b.memorySvc != nil {
		if err := b.memorySvc.Close(); err != nil {
			slog.Error("failed to close memory service", "error", err)
		}
	}
}

// ResolveResumeChoice implements domain.ResumeHandler.
func (b *BuiltinPlugin) ResolveResumeChoice(
	ctx context.Context,
	chat domain.ChatRef,
	sessionIndex int,
) (*domain.SessionInfo, error) {
	if b.resumeStore == nil {
		return nil, nil
	}
	s, ok := b.resumeStore.Get(chat, sessionIndex)
	if !ok {
		return nil, nil
	}
	if err := b.sessionMgr.LoadSession(ctx, chat, s.SessionID, s.Workspace); err != nil {
		return nil, err
	}
	return s, nil
}

// ResolveSession implements domain.SessionResolver.
func (b *BuiltinPlugin) ResolveSession(ctx context.Context, msg domain.InboundMessage) (string, error) {
	if info := b.sessionMgr.ActiveSession(msg.ChatRef); info != nil {
		return info.SessionID, nil
	}
	// Agent process lifecycle (spawn + initialize + new_session) must not be bounded
	// by the inbound message context, which may carry a short ACK deadline (e.g. WPS
	// event SDK). Use a background context with ConnectTimeout so a cold-start agent
	// does not fail with "context deadline exceeded" on the first message.
	connectTimeout := time.Duration(b.cfg.Agent.ConnectTimeout) * time.Second
	if connectTimeout <= 0 {
		connectTimeout = 30 * time.Second
	}
	spawnCtx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := b.sessionMgr.NewSession(spawnCtx, msg.ChatRef, b.defaultWorkspace()); err != nil {
		return "", err
	}
	if info := b.sessionMgr.ActiveSession(msg.ChatRef); info != nil {
		return info.SessionID, nil
	}
	return "", nil
}

// RouteMessage implements domain.MessageRouter.
func (b *BuiltinPlugin) RouteMessage(
	ctx context.Context,
	msg domain.InboundMessage,
	state domain.State,
) (domain.Action, error) {
	text := strings.TrimSpace(msg.Text)
	if !strings.HasPrefix(text, "/") {
		return domain.Action{Kind: domain.ActionPrompt, Input: convertToPromptInput(msg)}, nil
	}
	parts := strings.Fields(text[1:])
	if len(parts) == 0 {
		return domain.Action{Kind: domain.ActionPrompt, Input: convertToPromptInput(msg)}, nil
	}
	args := parts[1:]
	if len(parts) == 1 {
		args = nil
	}
	return domain.Action{Kind: domain.ActionCommand, Command: strings.ToLower(parts[0]), Args: args}, nil
}

// ExecuteAction implements domain.ActionExecutor.
func (b *BuiltinPlugin) ExecuteAction(
	ctx context.Context,
	action domain.Action,
	tc *domain.TurnContext,
) (*domain.Result, error) {
	if action.Kind != domain.ActionPrompt {
		return nil, nil
	}
	if b.prompter == nil {
		return &domain.Result{Text: "Agent not configured."}, nil
	}
	job := &promptJob{action: action, tc: tc}
	if !b.queue.Submit(job) {
		if tgchannel.IsBackgroundResponder(tc.Responder) || wxchannel.IsBackgroundResponder(tc.Responder) {
			logQueueFullRejected(tc.Chat, "cron")
			return &domain.Result{SuppressOutbound: true}, nil
		}
		return &domain.Result{Text: promptQueueFullMessage}, nil
	}
	return &domain.Result{SuppressOutbound: true}, nil
}

func (b *BuiltinPlugin) completePromptJob(ctx context.Context, job *promptJob) {
	runCtx := ctx
	var cancel context.CancelFunc
	if sec := b.cfg.Agent.PromptQueue.JobTimeoutSeconds; sec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(sec)*time.Second)
		defer cancel()
	}
	result := b.executor.runPromptJob(runCtx, job)
	if result != nil {
		b.fw.RenderAndDispatch(ctx, result, job.tc.State, job.tc.Responder)
	}
	if err := b.SaveState(ctx, job.tc.SessionID, job.tc.State); err != nil {
		slog.Warn("SaveState after prompt job", "error", err)
	}
}

func (b *BuiltinPlugin) cancelPromptQueueAndRunning(ctx context.Context, chat domain.ChatRef) (int, error) {
	if b.prompter == nil {
		return 0, domain.ErrNoActiveSession
	}
	if b.queue == nil {
		return 0, b.prompter.Cancel(ctx, chat)
	}
	return b.queue.CancelAndDrain(chat.CompositeKey(), func() error {
		return b.prompter.Cancel(ctx, chat)
	})
}

// HandleBusySendNow implements domain.BusyHandler.
func (b *BuiltinPlugin) HandleBusySendNow(chat domain.ChatRef, token string) (bool, error) {
	if b.prompter == nil {
		return false, nil
	}
	if b.queue == nil || !b.queue.BusyTokenMatches(chat.CompositeKey(), token) {
		return false, nil
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), prompterCancelTimeout)
	defer cancel()
	if err := b.prompter.Cancel(cancelCtx, chat); err != nil {
		return false, err
	}
	return true, nil
}

// OnError implements domain.ErrorObserver.
func (b *BuiltinPlugin) OnError(ctx context.Context, stage string, err error, msg domain.InboundMessage) {
	slog.Error("turn error", "stage", stage, "chat", msg.CompositeKey(), "error", err)
}

// RenderOutbound implements domain.OutboundRenderer.
func (b *BuiltinPlugin) RenderOutbound(
	ctx context.Context,
	result *domain.Result,
	state domain.State,
) ([]domain.OutboundMessage, error) {
	if result.Reply == nil {
		return nil, nil
	}
	return []domain.OutboundMessage{{
		Text:     result.Reply.Text,
		Markdown: true,
		Images:   result.Reply.Images,
		Files:    result.Reply.Files,
	}}, nil
}

// DispatchOutbound implements domain.OutboundDispatcher.
func (b *BuiltinPlugin) DispatchOutbound(ctx context.Context, msg domain.OutboundMessage, resp domain.Responder) error {
	return resp.Reply(msg)
}

func convertToPromptInput(msg domain.InboundMessage) domain.PromptInput {
	input := domain.PromptInput{Text: msg.Text}
	for _, att := range msg.Attachments {
		if att.MediaType == "image" {
			input.Images = append(input.Images, domain.ImageData{
				MIMEType: "image/jpeg",
				Data:     att.Data,
				Name:     att.FileName,
			})
		} else {
			input.Files = append(input.Files, convertAttachmentToFileData(att))
		}
	}
	return input
}

func convertAttachmentToFileData(att domain.Attachment) domain.FileData {
	fd := domain.FileData{MIMEType: att.MediaType, Data: att.Data, Name: att.FileName}
	if utf8.Valid(att.Data) {
		s := string(att.Data)
		fd.TextContent = &s
	}
	if fd.MIMEType == "" {
		fd.MIMEType = "application/octet-stream"
	}
	if fd.Name == "" {
		fd.Name = "attachment.bin"
	}
	return fd
}

func episodeID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("2006-01-02-150405") + "-" + hex.EncodeToString(b[:])
}

func extractTitleFromSummary(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "title:") {
			title := strings.TrimSpace(strings.TrimPrefix(line, "title:"))
			title = strings.Trim(title, "\"")
			if title != "" {
				return title
			}
		}
	}
	return "Session summary"
}

const (
	minTranscriptLen = 100 // byte length; ~33 CJK chars or ~100 ASCII chars
	summarizeTimeout = 30 * time.Second
)

func (b *BuiltinPlugin) buildBeforeSessionSwitch() func(ctx context.Context, chat domain.ChatRef) {
	if b.memorySvc == nil {
		return nil
	}
	summarizer := agent.NewAgentSummarizer(b.prompter)
	return func(ctx context.Context, chat domain.ChatRef) {
		b.summarizeSessionBeforeSwitch(ctx, chat, summarizer)
	}
}

func (b *BuiltinPlugin) summarizeSessionBeforeSwitch(
	ctx context.Context,
	chat domain.ChatRef,
	summarizer *agent.AgentSummarizer,
) {
	if b.sessionMgr.ActiveSession(chat) == nil {
		return
	}
	chatKey := chat.CompositeKey()
	transcript, spans, err := b.memorySvc.ReadUnsummarizedWithSpans(chatKey)
	if err != nil {
		slog.Warn("failed to read unsummarized history", "chat", chatKey, "error", err)
		return
	}
	if len(transcript) < minTranscriptLen {
		return
	}
	sumCtx, cancel := context.WithTimeout(ctx, summarizeTimeout)
	defer cancel()
	summary, err := summarizer.Summarize(sumCtx, chat, transcript)
	if err != nil {
		slog.Warn("session summarization failed", "chat", chatKey, "error", err)
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	title := extractTitleFromSummary(summary)
	if !strings.HasPrefix(summary, "---") {
		slog.Warn("summary missing YAML front matter, raw references will be lost", "chat", chatKey)
	}
	summary = memory.InsertRawReferences(summary, chatKey, spans)
	entry := domain.MemoryEntry{
		ID:       episodeID(),
		Category: "episode",
		Title:    title,
		Content:  summary,
		Date:     time.Now().Format("2006-01-02"),
	}
	if err := b.memorySvc.Save(entry); err != nil {
		slog.Warn("failed to save session episode", "chat", chatKey, "error", err)
		return
	}
	if err := b.memorySvc.MarkSummarized(chatKey); err != nil {
		slog.Warn("failed to mark history as summarized", "chat", chatKey, "error", err)
	}
	slog.Info("session summarized", "chat", chatKey, "episode", entry.ID, "title", title)
}

func (b *BuiltinPlugin) buildAgentService() {
	exe := b.getExecutablePath()
	svcCfg := b.buildAgentServiceConfig(exe)
	svc := b.createAgentService(svcCfg)
	b.sessionMgr = svc
	b.prompter = svc
	b.permHandler = svc
	b.actObserver = svc
	b.modelMgr = svc
	b.modeMgr = svc
	b.shutdownFn = svc.Shutdown
}

func (b *BuiltinPlugin) getExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("failed to get executable path", "error", err)
		return os.Args[0]
	}
	return exe
}

func (b *BuiltinPlugin) buildAgentServiceConfig(exe string) agent.ServiceConfig {
	return agent.ServiceConfig{
		AgentCommand:   strings.Fields(b.cfg.Agent.Command),
		Workspace:      b.cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(b.cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: domain.PermissionMode(b.cfg.Permissions.Mode),
		EventOutput:    b.cfg.Permissions.EventOutput,
		DefaultModel:   b.cfg.Agent.Model,
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "acpclaw-tools",
					Command: exe,
					Args:    []string{"mcp"},
					Env:     []acpsdk.EnvVariable{},
				},
			},
		},
	}
}

type agentBundle interface {
	domain.SessionManager
	domain.Prompter
	domain.PermissionHandler
	domain.ActivityObserver
	domain.ModelManager
	domain.ModeManager
}

func (b *BuiltinPlugin) createAgentService(svcCfg agent.ServiceConfig) agentBundle {
	if b.echo {
		slog.Info("echo mode enabled: using EchoAgentService")
		return agent.NewEchoAgentService()
	}
	return agent.NewAgentService(svcCfg)
}

// CreateTelegramBot creates the Telegram bot from config.
func (b *BuiltinPlugin) CreateTelegramBot() (*telego.Bot, error) {
	var opts []telego.BotOption
	if b.cfg.Telegram.Proxy != "" {
		httpClient, err := buildProxyHTTPClient(b.cfg.Telegram.Proxy)
		if err != nil {
			return nil, err
		}
		opts = append(opts, telego.WithHTTPClient(httpClient))
		slog.Info("using proxy", "proxy", b.cfg.Telegram.Proxy)
	}
	opts = append(opts, telego.WithLogger(telegoLogger{}))
	return telego.NewBot(b.cfg.Telegram.Token, opts...)
}

// PrepareTelegramChannel creates and sets the Telegram channel. Call before Framework.Init().
func (b *BuiltinPlugin) PrepareTelegramChannel(bot *telego.Bot, updates <-chan telego.Update, fw *framework.Framework) {
	b.tgChannel = b.buildTelegramChannel(bot, updates, fw)
}

func buildProxyHTTPClient(proxyAddr string) (*http.Client, error) {
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, err
	}
	transport, err := buildProxyTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport}, nil
}

func buildProxyTransport(proxyURL *url.URL) (*http.Transport, error) {
	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		return &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}, nil
	}
	return &http.Transport{Proxy: http.ProxyURL(proxyURL)}, nil
}

type telegoLogger struct{}

func (telegoLogger) Debugf(format string, args ...any) {
	slog.Debug(fmt.Sprintf(format, args...))
}

func (telegoLogger) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if isShutdownNoise(msg) {
		slog.Debug("telego shutdown", "msg", msg)
		return
	}
	slog.Error(msg)
}

func isShutdownNoise(msg string) bool {
	return strings.Contains(msg, "interrupt signal") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded")
}

func (b *BuiltinPlugin) buildTelegramChannel(
	bot *telego.Bot,
	updates <-chan telego.Update,
	fw *framework.Framework,
) *tgchannel.TelegramChannel {
	ids := b.cfg.Telegram.AllowedUserIDs
	names := b.cfg.Telegram.AllowedUsernames
	allowlist := tgchannel.AllowlistConfig{AllowedUserIDs: ids, AllowedUsernames: names}
	channelCfg := tgchannel.ChannelConfig{AllowedUserIDs: ids, AllowedUsernames: names}
	return tgchannel.NewTelegramChannel(bot, updates, channelCfg, fw, tgchannel.NewAllowlistChecker(allowlist))
}

// PrepareWeixinChannel creates and sets the WeChat channel. Call before Framework.Init().
func (b *BuiltinPlugin) PrepareWeixinChannel(bot *weixinbot.Bot) {
	b.wxChannel = wxchannel.NewWeixinChannel(bot)
}
