package builtin

import (
	"context"
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
	"github.com/zhu327/acpclaw/internal/agent"
	"github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	"github.com/zhu327/acpclaw/internal/builtin/commands"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/framework"
	"golang.org/x/net/proxy"
)

// BuiltinPlugin provides the default implementation of all framework hooks.
type BuiltinPlugin struct {
	cfg         *config.Config
	echo        bool
	fw          *framework.Framework
	agentSvc    domain.AgentService
	tgChannel   *telegram.TelegramChannel
	adapter     *commands.AgentAdapter
	resumeStore commands.ResumeChoicesStore
	executor    *promptExecutor
}

// NewPlugin creates a new BuiltinPlugin.
func NewPlugin(cfg *config.Config, echoMode bool) (*BuiltinPlugin, error) {
	return &BuiltinPlugin{cfg: cfg, echo: echoMode}, nil
}

// Name implements framework.Plugin.
func (b *BuiltinPlugin) Name() string { return "builtin" }

// Init implements domain.PluginInitializer.
func (b *BuiltinPlugin) Init(fw any) error {
	f, ok := fw.(*framework.Framework)
	if !ok {
		return nil
	}
	b.fw = f
	b.buildAgentService()
	b.adapter = commands.NewAgentAdapter(b.agentSvc)
	b.resumeStore = commands.NewResumeChoicesStore()
	b.executor = newPromptExecutor(b.agentSvc, f, b.buildFirstPromptPrefix)
	b.wireAgentCallbacks()
	return nil
}

func (b *BuiltinPlugin) buildFirstPromptPrefix(chatID string) string {
	// Memory context will be added in Phase 5
	return buildSessionInfoBlock(chatID, "telegram")
}

func (b *BuiltinPlugin) wireAgentCallbacks() {
	b.agentSvc.SetPermissionHandler(func(chatID string, req domain.PermissionRequest) <-chan domain.PermissionResponse {
		chat := domain.ChatRef{ChannelKind: "telegram", ChatID: chatID}
		ch := b.fw.RegisterPendingPermission(req.ID, chat)
		if resp := b.fw.GetResponder(chat); resp != nil {
			_ = resp.ShowPermissionUI(domain.ChannelPermissionRequest{
				ID:               req.ID,
				Tool:             req.Tool,
				Description:      req.Description,
				AvailableActions: permDecisionsToStrings(req.AvailableActions),
			})
		}
		return ch
	})
	b.agentSvc.SetActivityHandler(func(chatID string, block domain.ActivityBlock) {
		chat := domain.ChatRef{ChannelKind: "telegram", ChatID: chatID}
		if resp := b.fw.GetResponder(chat); resp != nil {
			workspace := ""
			if info := b.agentSvc.ActiveSession(chatID); info != nil {
				workspace = info.Workspace
			}
			b := block
			b.Workspace = workspace
			_ = resp.SendActivity(b)
		}
	})
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

// Commands implements domain.CommandProvider.
func (b *BuiltinPlugin) Commands() []domain.Command {
	if b.adapter == nil {
		return nil
	}
	defaultWs := b.cfg.Agent.Workspace
	if defaultWs == "" {
		defaultWs = "."
	}
	return []domain.Command{
		commands.NewStartCommand(),
		commands.NewHelpCommand(),
		commands.NewNewCommand(b.adapter, defaultWs),
		commands.NewSessionCommand(b.adapter),
		commands.NewResumeCommand(b.adapter, b.resumeStore),
		commands.NewCancelCommand(b.adapter),
		commands.NewReconnectCommand(b.adapter, defaultWs),
		commands.NewStatusCommand(b.adapter),
	}
}

// Shutdown stops the agent service. Call on process exit.
func (b *BuiltinPlugin) Shutdown() {
	if b.agentSvc != nil {
		b.agentSvc.Shutdown()
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
	chatID := chat.CompositeKey()
	if err := b.agentSvc.LoadSession(ctx, chatID, s.SessionID, s.Workspace); err != nil {
		return nil, err
	}
	return s, nil
}

// ResolveSession implements domain.SessionResolver.
func (b *BuiltinPlugin) ResolveSession(ctx context.Context, msg domain.InboundMessage) (string, error) {
	chatID := msg.ChatRef.CompositeKey()
	info := b.agentSvc.ActiveSession(chatID)
	if info != nil {
		return info.SessionID, nil
	}
	workspace := b.cfg.Agent.Workspace
	if workspace == "" {
		workspace = "."
	}
	if err := b.agentSvc.NewSession(ctx, chatID, workspace); err != nil {
		return "", err
	}
	info = b.agentSvc.ActiveSession(chatID)
	if info != nil {
		return info.SessionID, nil
	}
	return "", nil
}

// RouteMessage implements domain.MessageRouter.
func (b *BuiltinPlugin) RouteMessage(ctx context.Context, msg domain.InboundMessage, state domain.State) (domain.Action, error) {
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text[1:])
		name := strings.ToLower(parts[0])
		var args []string
		if len(parts) > 1 {
			args = parts[1:]
		}
		return domain.Action{Kind: domain.ActionCommand, Command: name, Args: args}, nil
	}
	return domain.Action{
		Kind:  domain.ActionPrompt,
		Input: convertToPromptInput(msg),
	}, nil
}

// ExecuteAction implements domain.ActionExecutor.
func (b *BuiltinPlugin) ExecuteAction(ctx context.Context, action domain.Action, tc *domain.TurnContext) (*domain.Result, error) {
	if action.Kind != domain.ActionPrompt {
		return nil, nil
	}
	if b.agentSvc == nil {
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
	slog.Error("turn error", "stage", stage, "chat", msg.ChatRef.CompositeKey(), "error", err)
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
		switch att.MediaType {
		case "image":
			input.Images = append(input.Images, domain.ImageData{
				MIMEType: "image/jpeg",
				Data:     att.Data,
				Name:     att.FileName,
			})
		default:
			fd := domain.FileData{
				MIMEType: att.MediaType,
				Data:     att.Data,
				Name:     att.FileName,
			}
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
			input.Files = append(input.Files, fd)
		}
	}
	return input
}

func (b *BuiltinPlugin) buildAgentService() {
	exe, err := os.Executable()
	if err != nil {
		slog.Error("failed to get executable path", "error", err)
		exe = os.Args[0]
	}

	agentCmd := strings.Fields(b.cfg.Agent.Command)
	svcCfg := agent.ServiceConfig{
		AgentCommand:   agentCmd,
		Workspace:      b.cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(b.cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: domain.PermissionMode(b.cfg.Permissions.Mode),
		EventOutput:    b.cfg.Permissions.EventOutput,
		ChannelName:    "telegram",
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "acpclaw-tools",
					Command: exe,
					Args:    []string{"mcp"},
				},
			},
		},
	}
	if b.echo {
		slog.Info("echo mode enabled: using EchoAgentService")
		b.agentSvc = agent.NewEchoAgentService()
	} else {
		b.agentSvc = agent.NewAgentService(svcCfg)
	}
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
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	}
	return &http.Client{Transport: transport}, nil
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
	allowlistCfg := telegram.AllowlistConfig{
		AllowedUserIDs:   b.cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: b.cfg.Telegram.AllowedUsernames,
	}
	tgCfg := telegram.ChannelConfig{
		AllowedUserIDs:   allowlistCfg.AllowedUserIDs,
		AllowedUsernames: allowlistCfg.AllowedUsernames,
	}
	return telegram.NewTelegramChannel(
		bot,
		updates,
		tgCfg,
		fw,
		telegram.NewAllowlistChecker(allowlistCfg),
	)
}
