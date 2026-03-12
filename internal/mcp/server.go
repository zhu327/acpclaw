package mcp

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	"github.com/zhu327/acpclaw/internal/memory"
)

// NewServer creates a minimal MCP server with a hello tool.
func NewServer() *server.MCPServer {
	return NewServerWithMemoryAndCron(nil, nil)
}

// NewServerWithMemoryAndCron creates an MCP server with hello + memory + cron tools.
func NewServerWithMemoryAndCron(memorySvc *memory.Service, cronStore *cron.Store) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")

	if memorySvc != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memorySvc))
		s.AddTool(memorySearchTool(), memorySearchHandler(memorySvc))
		s.AddTool(memorySaveTool(), memorySaveHandler(memorySvc))
		s.AddTool(memoryListTool(), memoryListHandler(memorySvc))
	}

	if cronStore != nil {
		s.AddTool(cronCreateTool(), cronCreateHandler(cronStore))
		s.AddTool(cronListTool(), cronListHandler(cronStore))
		s.AddTool(cronDeleteTool(), cronDeleteHandler(cronStore))
	}
	return s
}
