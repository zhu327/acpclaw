package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/server"
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

	s := internalmcp.NewServerWithMemory(memorySvc)
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
