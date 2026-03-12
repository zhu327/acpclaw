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
	"strings"
	"syscall"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/mymmrac/telego"
	"github.com/zhu327/acpclaw/internal/acp"
	"github.com/zhu327/acpclaw/internal/bot"
	"github.com/zhu327/acpclaw/internal/config"
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

	agentCmd := strings.Fields(cfg.Agent.Command)
	svcCfg := acp.ServiceConfig{
		AgentCommand:   agentCmd,
		Workspace:      cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: acp.PermissionMode(cfg.Permissions.Mode),
		EventOutput:    cfg.Permissions.EventOutput,
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "hello",
					Command: mcpChannelPath,
					Args:    []string{},
					Env:     []acpsdk.EnvVariable{},
				},
			},
		},
	}
	var agentSvc acp.AgentService
	if *echoMode {
		slog.Info("echo mode enabled: using EchoAgentService")
		agentSvc = acp.NewEchoAgentService()
	} else {
		agentSvc = acp.NewAgentService(svcCfg)
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

	botCfg := bot.Config{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
		DefaultWorkspace: cfg.Agent.Workspace,
	}
	bridge := bot.NewBridge(telegoBot, agentSvc, botCfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer agentSvc.Shutdown()

	updates, err := telegoBot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		AllowedUpdates: []string{"message", "callback_query"},
	}, telego.WithLongPollingRetryTimeout(0))
	if err != nil {
		return fmt.Errorf("starting long polling: %w", err)
	}

	if err := bridge.RegisterHandlers(updates); err != nil {
		return fmt.Errorf("registering handlers: %w", err)
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return bridge.Run(gCtx)
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
