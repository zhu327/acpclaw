package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	internalmcp "github.com/zhu327/acpclaw/internal/mcp"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/util"
)

func main() {
	memoryDir := util.ExpandPath(os.Getenv("ACPCLAW_MEMORY_DIR"))
	historyDir := util.ExpandPath(os.Getenv("ACPCLAW_HISTORY_DIR"))

	// Set defaults if not provided
	home, _ := os.UserHomeDir()
	if memoryDir == "" {
		memoryDir = filepath.Join(home, ".acpclaw", "memory")
	}
	if historyDir == "" {
		historyDir = filepath.Join(home, ".acpclaw", "history")
	}

	var memorySvc *memory.Service
	var err error
	memorySvc, err = memory.NewService(memoryDir, historyDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Memory service init error: %v\n", err)
	} else {
		_ = memorySvc.Reindex()
		defer func() {
			if err := memorySvc.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Memory service close error: %v\n", err)
			}
		}()
	}

	cronDir := util.ExpandPath(os.Getenv("ACPCLAW_CRON_DIR"))
	if cronDir == "" {
		cronDir = filepath.Join(home, ".acpclaw", "cron")
	}
	cronStore := cron.NewStore(cronDir)

	s := internalmcp.NewServerWithMemoryAndCron(memorySvc, cronStore)
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
