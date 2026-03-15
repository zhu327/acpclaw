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
	"github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	"github.com/zhu327/acpclaw/internal/builtin/commands"
	"github.com/zhu327/acpclaw/internal/builtin/memory"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/framework"
	"github.com/zhu327/acpclaw/internal/templates"
	"golang.org/x/net/proxy"
)

// BuiltinPlugin provides the default implementation of all framework hooks.
type BuiltinPlugin struct {
	cfg         *config.Config
	echo        bool
	fw          domain.PluginContext
	sessionMgr  domain.SessionManager
	prompter    domain.Prompter
	permHandler domain.PermissionHandler
	actObserver domain.ActivityObserver
	tgChannel   *telegram.TelegramChannel
	resumeStore commands.ResumeChoicesStore
	executor    *promptExecutor
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

func (b *BuiltinPlugin) handlePermissionRequest(chat domain.ChatRef, req domain.PermissionRequest) <-chan domain.PermissionResponse {
	ch := b.fw.RegisterPendingPermission(req.ID, chat)
	if resp := b.fw.GetResponder(chat); resp != nil {
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
	resp := b.fw.GetResponder(chat)
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
	if b.tgChannel != nil {
		return []domain.Channel{b.tgChannel}
	}
	return nil
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
		commands.NewCancelCommand(b.prompter),
		commands.NewReconnectCommand(b.sessionMgr, defaultWs, beforeSwitch),
		commands.NewStatusCommand(b.sessionMgr),
	}
}

// StartBackgroundTasks starts background tasks like memory reindex. Call after Init.
func (b *BuiltinPlugin) StartBackgroundTasks(ctx context.Context) {
	if b.memorySvc != nil {
		go b.memorySvc.StartPeriodicReindex(ctx)
	}
}

// Shutdown stops the agent service and closes memory. Call on process exit.
func (b *BuiltinPlugin) Shutdown() {
	if b.shutdownFn != nil {
		b.shutdownFn()
	}
	if b.memorySvc != nil {
		if err := b.memorySvc.Close(); err != nil {
			slog.Error("failed to close memory service", "error", err)
		}
	}
}

// ResolveResumeChoice implements domain.ResumeHandler.
func (b *BuiltinPlugin) ResolveResumeChoice(ctx context.Context, chat domain.ChatRef, sessionIndex int) (*domain.SessionInfo, error) {
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
	if err := b.sessionMgr.NewSession(ctx, msg.ChatRef, b.defaultWorkspace()); err != nil {
		return "", err
	}
	if info := b.sessionMgr.ActiveSession(msg.ChatRef); info != nil {
		return info.SessionID, nil
	}
	return "", nil
}

// RouteMessage implements domain.MessageRouter.
func (b *BuiltinPlugin) RouteMessage(ctx context.Context, msg domain.InboundMessage, state domain.State) (domain.Action, error) {
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
func (b *BuiltinPlugin) ExecuteAction(ctx context.Context, action domain.Action, tc *domain.TurnContext) (*domain.Result, error) {
	if action.Kind != domain.ActionPrompt {
		return nil, nil
	}
	if b.prompter == nil {
		return &domain.Result{Text: "Agent not configured."}, nil
	}
	result := b.executor.executePrompt(ctx, action, tc)
	return result, nil
}

// HandleBusySendNow implements domain.BusyHandler.
func (b *BuiltinPlugin) HandleBusySendNow(chat domain.ChatRef, token string) (bool, error) {
	if b.executor == nil {
		return false, nil
	}
	return b.executor.HandleBusySendNow(chat, token)
}

// OnError implements domain.ErrorObserver.
func (b *BuiltinPlugin) OnError(ctx context.Context, stage string, err error, msg domain.InboundMessage) {
	slog.Error("turn error", "stage", stage, "chat", msg.CompositeKey(), "error", err)
}

// RenderOutbound implements domain.OutboundRenderer.
func (b *BuiltinPlugin) RenderOutbound(ctx context.Context, result *domain.Result, state domain.State) ([]domain.OutboundMessage, error) {
	if result.Reply == nil {
		return nil, nil
	}
	return []domain.OutboundMessage{{
		Text:   result.Reply.Text,
		Images: result.Reply.Images,
		Files:  result.Reply.Files,
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

func (b *BuiltinPlugin) summarizeSessionBeforeSwitch(ctx context.Context, chat domain.ChatRef, summarizer *agent.AgentSummarizer) {
	if b.sessionMgr.ActiveSession(chat) == nil {
		return
	}
	chatKey := chat.CompositeKey()
	transcript, err := b.memorySvc.ReadUnsummarized(chatKey)
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
	if strings.TrimSpace(summary) == "" {
		return
	}
	title := extractTitleFromSummary(summary)
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

func (b *BuiltinPlugin) buildTelegramChannel(bot *telego.Bot, updates <-chan telego.Update, fw *framework.Framework) *telegram.TelegramChannel {
	ids := b.cfg.Telegram.AllowedUserIDs
	names := b.cfg.Telegram.AllowedUsernames
	allowlist := telegram.AllowlistConfig{AllowedUserIDs: ids, AllowedUsernames: names}
	channelCfg := telegram.ChannelConfig{AllowedUserIDs: ids, AllowedUsernames: names}
	return telegram.NewTelegramChannel(bot, updates, channelCfg, fw, telegram.NewAllowlistChecker(allowlist))
}
