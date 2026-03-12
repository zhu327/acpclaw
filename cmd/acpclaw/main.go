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
	flag.Parse()

	// Load config (fallback to env-only only when file is not found)
	cfg, err := config.Load(*configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		cfg, err = config.Load("")
		if err != nil {
			return err
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Logger
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)}
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	mcpChannelPath := filepath.Join(filepath.Dir(exe), "mcp-channel")

	// State file path (used by service to write, and by mcp-channel to read)
	statePath := os.Getenv("ACP_TELEGRAM_CHANNEL_STATE_FILE") // TODO 确认下这里后续可以去掉吗, 一个chat id一个acp进程的模式来改造
	if statePath == "" {
		statePath = fmt.Sprintf("%s/telegram-acp-bot-mcp-state-%d.json", os.TempDir(), os.Getpid())
	}

	// MCP server env
	mcpEnv := map[string]string{ // TODO 重新规划下mcp的模式, 可能不需要这些参数
		"ACP_TELEGRAM_BOT_TOKEN":          cfg.Telegram.Token,
		"ACP_TELEGRAM_CHANNEL_STATE_FILE": statePath,
	}
	if v := os.Getenv("ACP_TELEGRAM_CHANNEL_ALLOW_PATH"); v != "" {
		mcpEnv["ACP_TELEGRAM_CHANNEL_ALLOW_PATH"] = v
		baseDir := os.Getenv("ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR")
		if baseDir == "" {
			if resolved, err := filepath.Abs(cfg.Agent.Workspace); err == nil {
				baseDir = resolved
			}
		}
		if baseDir != "" {
			mcpEnv["ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR"] = baseDir
		}
	}

	// ACP service config
	agentCmd := strings.Fields(cfg.Agent.Command)
	mcpEnvVars := make([]acpsdk.EnvVariable, 0, len(mcpEnv)+1)
	for k, v := range mcpEnv {
		mcpEnvVars = append(mcpEnvVars, acpsdk.EnvVariable{Name: k, Value: v})
	}
	svcCfg := acp.ServiceConfig{
		AgentCommand:   agentCmd,
		Workspace:      cfg.Agent.Workspace,
		ConnectTimeout: time.Duration(cfg.Agent.ConnectTimeout) * time.Second,
		PermissionMode: acp.PermissionMode(cfg.Permissions.Mode),
		EventOutput:    cfg.Permissions.EventOutput,
		MCPServers: []acpsdk.McpServer{
			{
				Stdio: &acpsdk.McpServerStdio{
					Name:    "telegram-channel",
					Command: mcpChannelPath,
					Args:    []string{},
					Env:     mcpEnvVars,
				},
			},
		},
	}
	agentSvc := acp.NewAgentService(svcCfg)

	// Telegram bot
	var botOpts []telego.BotOption
	if cfg.Telegram.Proxy != "" { // 可选的代理设置
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

	// Bridge
	botCfg := bot.Config{
		AllowedUserIDs:   cfg.Telegram.AllowedUserIDs,
		AllowedUsernames: cfg.Telegram.AllowedUsernames,
		DefaultWorkspace: cfg.Agent.Workspace,
		MCPStatePath:     statePath,
		RestartCommand:   cfg.Agent.RestartCommand,
	}
	bridge := bot.NewBridge(telegoBot, agentSvc, botCfg)

	// Graceful shutdown
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

// buildProxyHTTPClient builds an HTTP client from a proxy URL, supporting socks5:// and http:// schemes.
func buildProxyHTTPClient(proxyAddr string) (*http.Client, error) {
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("parsing proxy URL %q: %w", proxyAddr, err)
	}

	var transport *http.Transport
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("creating SOCKS5 dialer: %w", err)
		}
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	default:
		transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}
	return &http.Client{Transport: transport}, nil
}

// telegoLogger bridges telego's Logger interface to slog, suppressing shutdown noise.
type telegoLogger struct{}

func (telegoLogger) Debugf(format string, args ...any) {
	slog.Debug(fmt.Sprintf(format, args...))
}

func (telegoLogger) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// Suppress expected errors during graceful shutdown (context cancellation / interrupt signal).
	if strings.Contains(msg, "interrupt signal") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") {
		slog.Debug("telego shutdown", "msg", msg)
		return
	}
	slog.Error(msg)
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
