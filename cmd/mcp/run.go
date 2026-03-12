package main

import (
	"log/slog"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	internalmcp "github.com/zhu327/acpclaw/internal/mcp"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/session"
	"github.com/zhu327/acpclaw/internal/util"
)

// acpclawPaths 持有 acpclaw 使用的三个目录路径
type acpclawPaths struct {
	memoryDir  string
	historyDir string
	cronDir    string
	contextDir string
}

func getAcpclawPaths() acpclawPaths {
	return acpclawPaths{
		memoryDir:  util.GetAcpclawMemoryDir(),
		historyDir: util.GetAcpclawHistoryDir(),
		cronDir:    util.GetAcpclawCronDir(),
		contextDir: util.GetAcpclawContextDir(),
	}
}

// initMemoryService 创建 memory.Service，失败时返回 (nil, nil, err)。
// 成功时返回 (svc, cleanup, nil)，调用方应 defer cleanup() 并在需要时调用 svc.Reindex()。
func initMemoryService(memoryDir, historyDir string) (*memory.Service, func(), error) {
	svc, err := memory.NewService(memoryDir, historyDir)
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
	sessionStore := session.NewStore(paths.contextDir)
	s := internalmcp.NewServerWithMemoryAndCron(memorySvc, cronStore, sessionStore)
	return server.ServeStdio(s)
}
