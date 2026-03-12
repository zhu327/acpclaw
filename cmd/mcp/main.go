package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
	internalmcp "github.com/zhu327/acpclaw/internal/mcp"
)

func main() {
	s := internalmcp.NewServer()
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
