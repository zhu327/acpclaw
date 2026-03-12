package mcp

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/session"
)

// NewServer creates a minimal MCP server with a hello tool.
func NewServer() *server.MCPServer {
	return NewServerWithMemoryAndCron(nil, nil, nil)
}

// NewServerWithMemoryAndCron creates an MCP server with hello + memory + cron tools.
func NewServerWithMemoryAndCron(
	memorySvc *memory.Service,
	cronStore *cron.Store,
	sessionStore *session.Store,
) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")

	if memorySvc != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memorySvc))
		s.AddTool(memorySearchTool(), memorySearchHandler(memorySvc))
		s.AddTool(memorySaveTool(), memorySaveHandler(memorySvc))
		s.AddTool(memoryListTool(), memoryListHandler(memorySvc))
	}

	if cronStore != nil && sessionStore != nil {
		s.AddTool(cronCreateTool(), cronCreateHandler(cronStore, sessionStore))
		s.AddTool(cronListTool(), cronListHandler(cronStore, sessionStore))
		s.AddTool(cronDeleteTool(), cronDeleteHandler(cronStore, sessionStore))
	}
	return s
}
