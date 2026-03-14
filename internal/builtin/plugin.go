package builtin

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/mymmrac/telego"
	"github.com/zhu327/acpclaw/internal/agent"
	"github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/framework"
)

// BuiltinPlugin provides the default implementation of all framework hooks.
type BuiltinPlugin struct {
	cfg       *config.Config
	echo      bool
	fw        *framework.Framework
	agentSvc  domain.AgentService
	tgChannel *telegram.TelegramChannel
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
	b.wireAgentCallbacks()
	return nil
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

// Commands implements domain.CommandProvider. Phase 2 returns empty; Phase 3 adds commands.
func (b *BuiltinPlugin) Commands() []domain.Command {
	return nil
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
	chatID := tc.Chat.CompositeKey()
	reply, err := b.agentSvc.Prompt(ctx, chatID, action.Input)
	if err != nil {
		return nil, err
	}
	return &domain.Result{Reply: reply}, nil
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

// PrepareTelegramChannel creates and sets the Telegram channel. Call before Framework.Init().
func (b *BuiltinPlugin) PrepareTelegramChannel(bot *telego.Bot, updates <-chan telego.Update, fw *framework.Framework) {
	b.tgChannel = b.buildTelegramChannel(bot, updates, fw)
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
