package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/zhu327/acpclaw/internal/config"
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

	app, err := SetupApp(cfg, *echoMode)
	if err != nil {
		return err
	}
	return app.Run()
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
