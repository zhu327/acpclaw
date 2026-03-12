package mcp

import (
	"github.com/mark3labs/mcp-go/server"
)

// NewServer creates the MCP channel server with two tools:
// telegram_channel_info and telegram_send_attachment.
// baseDir is the allowed base directory for path-mode attachments; empty means path mode is disabled.
func NewServer(store *StateStore, token string, allowPath bool, baseDir string) *server.MCPServer {
	s := server.NewMCPServer("Telegram ACP Channel", "1.0.0")
	s.AddTool(channelInfoTool(), channelInfoHandler())
	s.AddTool(sendAttachmentTool(allowPath), makeSendAttachmentHandler(store, token, allowPath, baseDir))
	return s
}
