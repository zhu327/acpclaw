package main

import (
	"context"
	"errors"
	"flag"
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
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "Path to YAML config file (optional)")
	echoMode := flag.Bool("echo", false, "Use EchoAgentService instead of real ACP agent (for testing)")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)}
	handler := slog.Handler(slog.NewTextHandler(os.Stderr, opts))
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	mcpChannelPath := filepath.Join(filepath.Dir(exe), "mcp-channel")

	// Resolve default .acpclaw directory paths.
	memoryDir := config.GetAcpclawMemoryDir()
	historyDir := config.GetAcpclawHistoryDir()
	cronDir := config.GetAcpclawCronDir()

	agentCmd := strings.Fields(cfg.Agent.Command)
	svcCfg := agent.ServiceConfig{
		AgentCommand:   agentCmd,
		Workspace:      cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: domain.PermissionMode(cfg.Permissions.Mode),
		EventOutput:    cfg.Permissions.EventOutput,
		ChannelName:    "telegram", // TODO: make configurable when supporting multiple channels
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
	var agentSvc domain.AgentService
	if *echoMode {
		slog.Info("echo mode enabled: using EchoAgentService")
		agentSvc = agent.NewEchoAgentService()
	} else {
		agentSvc = agent.NewAgentService(svcCfg)
	}

	var botOpts []telego.BotOption
	if cfg.Telegram.Proxy != "" {
		httpClient, err := buildProxyHTTPClient(cfg.Telegram.Proxy)
		if err != nil {
			return fmt.Errorf("configuring proxy: %w", err)
		}
		botOpts = append(botOpts, telego.WithHTTPClient(httpClient))
		slog.Info("using proxy", "proxy", cfg.Telegram.Proxy)
	}
	botOpts = append(botOpts, telego.WithLogger(telegoLogger{}))
	telegoBot, err := telego.NewBot(cfg.Telegram.Token, botOpts...)
	if err != nil {
		return fmt.Errorf("creating bot: %w", err)
	}

	dispCfg := dispatcher.Config{
		DefaultWorkspace: cfg.Agent.Workspace,
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
		AutoSummarize:    cfg.Memory.AutoSummarize,
	}
	disp := dispatcher.New(dispCfg)
	disp.SetAgentService(agentSvc)

	if cfg.Memory.Enabled {
		memorySvc, err := memory.NewService(memoryDir, historyDir)
		if err != nil {
			slog.Warn("memory service init failed", "error", err)
		} else {
			_ = memorySvc.Reindex()
			disp.SetMemoryService(memorySvc)
			defer func() {
				if err := memorySvc.Close(); err != nil {
					slog.Error("failed to close memory service", "error", err)
				}
			}()
			slog.Info("memory service enabled", "dir", memoryDir)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer agentSvc.Shutdown()

	updates, err := telegoBot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		AllowedUpdates: []string{"message", "callback_query"},
	}, telego.WithLongPollingRetryTimeout(0))
	if err != nil {
		return fmt.Errorf("starting long polling: %w", err)
	}

	tgCfg := telegram.ChannelConfig{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
	}
	allowlistChecker := dispatcher.NewAllowlistChecker(dispatcher.AllowlistConfig{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
	})
	tgChannel := telegram.NewTelegramChannel(
		telegoBot,
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

	g, gCtx := errgroup.WithContext(ctx)
	_ = gCtx

	if cfg.Cron.Enabled {
		cronStore := cron.NewStore(cronDir)
		scheduler := cron.NewScheduler(cronStore, 30*time.Second)

		scheduler.OnTrigger(func(job cron.Job) {
			if job.Channel != "telegram" {
				slog.Warn("unsupported cron job channel", "channel", job.Channel, "id", job.ID)
				return
			}

			msg := domain.InboundMessage{
				ChatID: job.ChatID,
				Text:   job.Message,
			}

			chatIDInt, _ := strconv.ParseInt(job.ChatID, 10, 64)
			resp := telegram.NewBackgroundResponder(telegoBot, chatIDInt)
			disp.Handle(msg, resp)
		})

		g.Go(func() error {
			scheduler.Start(gCtx)
			return nil
		})
		slog.Info("cron scheduler started", "dir", cronDir)
	}

	g.Go(func() error {
		return tgChannel.Start(disp.Handle)
	})

	slog.Info("bot started", "workspace", cfg.Agent.Workspace)
	return g.Wait()
}

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return config.Load("")
}

func buildProxyHTTPClient(proxyAddr string) (*http.Client, error) {
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("parsing proxy URL %q: %w", proxyAddr, err)
	}

	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("creating SOCKS5 dialer: %w", err)
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

var logLevels = map[string]slog.Level{
	"debug":   slog.LevelDebug,
	"warn":    slog.LevelWarn,
	"warning": slog.LevelWarn,
	"error":   slog.LevelError,
}

func parseLogLevel(level string) slog.Level {
	if l, ok := logLevels[strings.ToLower(level)]; ok {
		return l
	}
	return slog.LevelInfo
}
