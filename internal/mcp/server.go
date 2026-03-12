package mcp

import (
	"github.com/mark3labs/mcp-go/server"
)

// NewServer creates a minimal MCP server with a hello tool.
func NewServer() *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")
	s.AddTool(helloTool(), helloHandler())
	return s
}
