package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
	internalmcp "github.com/zhu327/acpclaw/internal/mcp"
)

func main() {
	token := os.Getenv("ACP_TELEGRAM_BOT_TOKEN")
	if token == "" {
		token = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "TELEGRAM_BOT_TOKEN or ACP_TELEGRAM_BOT_TOKEN is required")
		os.Exit(1)
	}

	statePath := os.Getenv("ACP_TELEGRAM_CHANNEL_STATE_FILE")
	if statePath == "" {
		statePath = fmt.Sprintf("%s/telegram-acp-bot-mcp-state-%d.json", os.TempDir(), os.Getppid())
	}

	allowPath := os.Getenv("ACP_TELEGRAM_CHANNEL_ALLOW_PATH") != ""
	baseDir := os.Getenv("ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR")

	store := internalmcp.NewStateStore(statePath)
	s := internalmcp.NewServer(store, token, allowPath, baseDir)
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
