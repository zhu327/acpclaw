package main

import (
	"log/slog"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/builtin/cron"
	internalmcp "github.com/zhu327/acpclaw/internal/builtin/mcp"
	"github.com/zhu327/acpclaw/internal/builtin/memory"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/templates"
)

func runMCP() error {
	memoryDir := config.GetAcpclawMemoryDir()
	historyDir := config.GetAcpclawHistoryDir()
	cronDir := config.GetAcpclawCronDir()

	memorySvc, err := memory.NewService(memoryDir, historyDir, templates.FS)
	if err != nil {
		slog.Warn("memory service init failed", "err", err)
	} else {
		defer func() {
			if closeErr := memorySvc.Close(); closeErr != nil {
				slog.Error("memory service close failed", "err", closeErr)
			}
		}()
		if reindexErr := memorySvc.Reindex(); reindexErr != nil {
			slog.Warn("reindex failed, using existing index", "err", reindexErr)
		}
	}

	cronStore := cron.NewStore(cronDir)
	s := internalmcp.NewServerWithMemoryAndCron(memorySvc, cronStore)
	return server.ServeStdio(s)
}
