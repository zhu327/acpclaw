package main

import (
	"log/slog"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/cron"
	internalmcp "github.com/zhu327/acpclaw/internal/mcp"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/templates"
)

// acpclawPaths holds the directory paths used by acpclaw.
type acpclawPaths struct {
	memoryDir  string
	historyDir string
	cronDir    string
}

func getAcpclawPaths() acpclawPaths {
	return acpclawPaths{
		memoryDir:  config.GetAcpclawMemoryDir(),
		historyDir: config.GetAcpclawHistoryDir(),
		cronDir:    config.GetAcpclawCronDir(),
	}
}

// initMemoryService creates a memory.Service; on failure returns (nil, nil, err).
// On success returns (svc, cleanup, nil); callers should defer cleanup() and call
// svc.Reindex() when needed.
func initMemoryService(memoryDir, historyDir string) (*memory.Service, func(), error) {
	svc, err := memory.NewService(memoryDir, historyDir, templates.FS)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if closeErr := svc.Close(); closeErr != nil {
			slog.Error("memory service close failed", "err", closeErr)
		}
	}
	return svc, cleanup, nil
}

func run() error {
	paths := getAcpclawPaths()

	memorySvc, cleanup, initErr := initMemoryService(paths.memoryDir, paths.historyDir)
	if initErr != nil {
		slog.Warn("memory service init failed", "err", initErr)
	} else {
		defer cleanup()
		if reindexErr := memorySvc.Reindex(); reindexErr != nil {
			slog.Warn("reindex failed, using existing index", "err", reindexErr)
		}
	}

	cronStore := cron.NewStore(paths.cronDir)
	s := internalmcp.NewServerWithMemoryAndCron(memorySvc, cronStore)
	return server.ServeStdio(s)
}
