package mcp

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/memory"
)

// NewServer creates a minimal MCP server with a hello tool.
func NewServer() *server.MCPServer {
	return NewServerWithMemory(nil)
}

// NewServerWithMemory creates an MCP server with hello + memory tools.
func NewServerWithMemory(memorySvc *memory.Service) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")
	s.AddTool(helloTool(), helloHandler())

	if memorySvc != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memorySvc))
		s.AddTool(memorySearchTool(), memorySearchHandler(memorySvc))
		s.AddTool(memorySaveTool(), memorySaveHandler(memorySvc))
		s.AddTool(memoryListTool(), memoryListHandler(memorySvc))
	}
	return s
}
