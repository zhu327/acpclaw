package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/mymmrac/telego"
	"github.com/zhu327/acpclaw/internal/agent"
	"github.com/zhu327/acpclaw/internal/channel/telegram"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/cron"
	"github.com/zhu327/acpclaw/internal/dispatcher"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/session"
	"github.com/zhu327/acpclaw/internal/templates"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
)

// App holds assembled application components and runs the bot.
type App struct {
	agentSvc  domain.AgentService
	memorySvc *memory.Service
	g         *errgroup.Group
}

// SetupApp assembles all components from config and returns a runnable App.
func SetupApp(cfg *config.Config, echoMode bool) (*App, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	mcpChannelPath := filepath.Join(filepath.Dir(exe), "mcp-channel")
	memoryDir := config.GetAcpclawMemoryDir()
	historyDir := config.GetAcpclawHistoryDir()
	cronDir := config.GetAcpclawCronDir()

	agentSvc := buildAgentService(cfg, echoMode, mcpChannelPath, memoryDir, historyDir, cronDir)
	bot, err := buildTelegramBot(cfg)
	if err != nil {
		return nil, err
	}

	disp := buildDispatcher(cfg, agentSvc)
	var memorySvc *memory.Service
	if cfg.Memory.Enabled {
		svc, err := memory.NewService(memoryDir, historyDir, templates.FS)
		if err != nil {
			slog.Warn("memory service init failed", "error", err)
		} else {
			_ = svc.Reindex()
			disp.SetMemoryService(svc)
			memorySvc = svc
			slog.Info("memory service enabled", "dir", memoryDir)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer agentSvc.Shutdown()

	updates, err := bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		AllowedUpdates: []string{"message", "callback_query"},
	}, telego.WithLongPollingRetryTimeout(0))
	if err != nil {
		return nil, err
	}

	tgChannel := buildTelegramChannel(cfg, bot, updates, disp)
	g, gCtx := errgroup.WithContext(ctx)

	if cfg.Cron.Enabled {
		setupCron(g, gCtx, cronDir, disp, bot)
		slog.Info("cron scheduler started", "dir", cronDir)
	}
	g.Go(func() error {
		return tgChannel.Start(disp.Handle)
	})

	slog.Info("bot started", "workspace", cfg.Agent.Workspace)
	return &App{agentSvc: agentSvc, memorySvc: memorySvc, g: g}, nil
}

// Run blocks until the app exits (e.g. on signal) and performs cleanup.
func (a *App) Run() error {
	if a.memorySvc != nil {
		defer func() {
			if err := a.memorySvc.Close(); err != nil {
				slog.Error("failed to close memory service", "error", err)
			}
		}()
	}
	return a.g.Wait()
}

func buildAgentService(
	cfg *config.Config,
	echoMode bool,
	mcpChannelPath, memoryDir, historyDir, cronDir string,
) domain.AgentService {
	agentCmd := strings.Fields(cfg.Agent.Command)
	svcCfg := agent.ServiceConfig{
		AgentCommand:   agentCmd,
		Workspace:      cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: domain.PermissionMode(cfg.Permissions.Mode),
		EventOutput:    cfg.Permissions.EventOutput,
		ChannelName:    "telegram",
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "acpclaw-memory",
					Command: mcpChannelPath,
					Args:    []string{},
					Env: func() []acpsdk.EnvVariable {
						var envs []acpsdk.EnvVariable
						if cfg.Memory.Enabled {
							envs = append(envs,
								acpsdk.EnvVariable{Name: "ACPCLAW_MEMORY_DIR", Value: memoryDir},
								acpsdk.EnvVariable{Name: "ACPCLAW_HISTORY_DIR", Value: historyDir},
							)
						}
						if cfg.Cron.Enabled {
							envs = append(envs, acpsdk.EnvVariable{Name: "ACPCLAW_CRON_DIR", Value: cronDir})
						}
						return envs
					}(),
				},
			},
		},
	}
	svcCfg.SessionStore = session.NewStore(config.GetAcpclawContextDir())
	if echoMode {
		slog.Info("echo mode enabled: using EchoAgentService")
		return agent.NewEchoAgentService()
	}
	return agent.NewAgentService(svcCfg)
}

func buildTelegramBot(cfg *config.Config) (*telego.Bot, error) {
	var opts []telego.BotOption
	if cfg.Telegram.Proxy != "" {
		httpClient, err := buildProxyHTTPClient(cfg.Telegram.Proxy)
		if err != nil {
			return nil, err
		}
		opts = append(opts, telego.WithHTTPClient(httpClient))
		slog.Info("using proxy", "proxy", cfg.Telegram.Proxy)
	}
	opts = append(opts, telego.WithLogger(telegoLogger{}))
	return telego.NewBot(cfg.Telegram.Token, opts...)
}

func buildDispatcher(cfg *config.Config, agentSvc domain.AgentService) *dispatcher.Dispatcher {
	dispCfg := dispatcher.Config{
		DefaultWorkspace: cfg.Agent.Workspace,
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
		AutoSummarize:    cfg.Memory.AutoSummarize,
		NewSummarizer: func(chatID string) domain.Summarizer {
			return agent.NewAgentSummarizer(agentSvc, chatID)
		},
	}
	disp := dispatcher.New(dispCfg)
	disp.SetAgentService(agentSvc)
	return disp
}

func buildTelegramChannel(
	cfg *config.Config,
	bot *telego.Bot,
	updates <-chan telego.Update,
	disp *dispatcher.Dispatcher,
) *telegram.TelegramChannel {
	tgCfg := telegram.ChannelConfig{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
	}
	allowlistChecker := dispatcher.NewAllowlistChecker(dispatcher.AllowlistConfig{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
	})
	return telegram.NewTelegramChannel(
		bot,
		updates,
		tgCfg,
		telegram.CallbackHandlers{
			OnPermission: func(reqID, decision string) {
				decisionMap := map[string]domain.PermissionDecision{
					"always": domain.PermissionAlways,
					"once":   domain.PermissionThisTime,
					"deny":   domain.PermissionDeny,
				}
				if d, ok := decisionMap[decision]; ok {
					disp.RespondPermission(reqID, d)
				}
			},
			OnBusySendNow:  disp.HandleBusySendNow,
			OnResumeChoice: disp.ResolveResumeChoice,
		},
		allowlistChecker,
	)
}

func setupCron(
	g *errgroup.Group,
	ctx context.Context,
	cronDir string,
	disp *dispatcher.Dispatcher,
	bot *telego.Bot,
) {
	cronStore := cron.NewStore(cronDir)
	scheduler := cron.NewScheduler(cronStore, 30*time.Second)
	scheduler.OnTrigger(func(job domain.CronJob) {
		if job.Channel != "telegram" {
			slog.Warn("unsupported cron job channel", "channel", job.Channel, "id", job.ID)
			return
		}
		msg := domain.InboundMessage{ChatID: job.ChatID, Text: job.Message}
		chatIDInt, _ := strconv.ParseInt(job.ChatID, 10, 64)
		resp := telegram.NewBackgroundResponder(bot, chatIDInt)
		disp.Handle(msg, resp)
	})
	g.Go(func() error {
		scheduler.Start(ctx)
		return nil
	})
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
