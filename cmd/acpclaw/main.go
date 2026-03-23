package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	"github.com/zhu327/acpclaw/internal/builtin"
	"github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	"github.com/zhu327/acpclaw/internal/builtin/channel/weixin"
	"github.com/zhu327/acpclaw/internal/builtin/cron"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/framework"
	weixinbot "github.com/zhu327/weixin-bot"
)

const usageText = `Usage: acpclaw [command] [flags]

Commands:
  mcp    Start MCP stdio server

Flags:
  -config string   Path to YAML config file (default "config.yaml")
  -echo            Use echo mode for testing

Run 'acpclaw mcp' to start the MCP server for agent integration.
`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(); err != nil {
			slog.Error("MCP server failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
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

	initLogging(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fw := framework.New()
	bp, err := builtin.NewPlugin(cfg, *echoMode)
	if err != nil {
		return err
	}
	defer bp.Shutdown()

	fw.Register(bp)

	// Telegram setup (conditional)
	var bot *telego.Bot
	if cfg.Telegram.Enabled {
		var err error
		bot, err = bp.CreateTelegramBot()
		if err != nil {
			return err
		}
		updates, err := bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
			AllowedUpdates: []string{"message", "callback_query"},
		}, telego.WithLongPollingRetryTimeout(0))
		if err != nil {
			return err
		}
		bp.PrepareTelegramChannel(bot, updates, fw)
	}

	// WeChat setup (conditional)
	var wxBot *weixinbot.Bot
	if cfg.Weixin.Enabled {
		wxBot = weixinbot.New(
			weixinbot.WithTokenPath(resolveWeixinTokenPath(cfg)),
			weixinbot.WithLogger(slog.Default()),
		)
		if _, err := wxBot.Login(ctx, weixinbot.LoginOptions{}); err != nil {
			return err
		}
		bp.PrepareWeixinChannel(wxBot)
	}

	if err := fw.Init(); err != nil {
		return err
	}

	bp.StartBackgroundTasks(ctx)

	if cfg.Cron.Enabled {
		startCronScheduler(ctx, cfg, bot, wxBot, fw)
	}

	slog.Info("bot started", "workspace", cfg.Agent.Workspace)
	return fw.Start(ctx)
}

func startCronScheduler(
	ctx context.Context,
	cfg *config.Config,
	tgBot *telego.Bot,
	wxBot *weixinbot.Bot,
	fw *framework.Framework,
) {
	cronDir := config.GetAcpclawCronDir()
	cronStore := cron.NewStore(cronDir)
	scheduler := cron.NewScheduler(cronStore, 30*time.Second)
	scheduler.OnTrigger(func(job domain.CronJob) {
		switch job.Channel {
		case "telegram":
			if tgBot == nil {
				slog.Warn("telegram not configured for cron job", "id", job.ID, "chat_id", job.ChatID)
				return
			}
			chatIDInt, _ := strconv.ParseInt(job.ChatID, 10, 64)
			resp := telegram.NewBackgroundResponder(ctx, tgBot, chatIDInt)
			msg := domain.InboundMessage{
				ChatRef: domain.ChatRef{ChannelKind: job.Channel, ChatID: job.ChatID},
				Text:    job.Message,
			}
			_ = fw.ProcessInbound(ctx, msg, resp)
		case "weixin":
			if wxBot == nil {
				slog.Warn("weixin not configured for cron job", "id", job.ID, "chat_id", job.ChatID)
				return
			}
			resp := weixin.NewBackgroundResponder(ctx, wxBot, job.ChatID)
			msg := domain.InboundMessage{
				ChatRef: domain.ChatRef{ChannelKind: job.Channel, ChatID: job.ChatID},
				Text:    job.Message,
			}
			_ = fw.ProcessInbound(ctx, msg, resp)
		default:
			slog.Warn("unsupported cron job channel", "channel", job.Channel, "id", job.ID)
		}
	})
	go scheduler.Start(ctx)
	slog.Info("cron scheduler started", "dir", cronDir)
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

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug, "warn": slog.LevelWarn,
	"warning": slog.LevelWarn, "error": slog.LevelError,
}

func initLogging(cfg *config.Config) {
	level := slog.LevelInfo
	if l, ok := logLevels[strings.ToLower(cfg.Logging.Level)]; ok {
		level = l
	}
	opts := &slog.HandlerOptions{Level: level}
	handler := slog.Handler(slog.NewTextHandler(os.Stderr, opts))
	if cfg.Logging.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func resolveWeixinTokenPath(cfg *config.Config) string {
	if cfg.Weixin.TokenPath != "" {
		return cfg.Weixin.TokenPath
	}
	return filepath.Join(config.GetAcpclawBaseDir(), "weixin-bot", "credentials.json")
}
