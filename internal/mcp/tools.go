package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

func helloTool() mcp.Tool {
	return mcp.NewTool("hello",
		mcp.WithDescription("Returns basic channel information"),
	)
}

func helloHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"message":"Hello from acpclaw"}`), nil
	}
}
