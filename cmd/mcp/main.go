package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := run(); err != nil {
		slog.Error("MCP server failed", "err", err)
		os.Exit(1)
	}
}
