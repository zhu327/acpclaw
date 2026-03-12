package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/zhu327/acpclaw/internal/util"
)

func validatePathWithinBase(path, baseDir string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be absolute")
	}
	baseDir = filepath.Clean(baseDir)
	if !filepath.IsAbs(baseDir) {
		return "", fmt.Errorf("ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR must be absolute")
	}
	if resolved, err := filepath.EvalSymlinks(baseDir); err == nil {
		baseDir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	rel, err := filepath.Rel(baseDir, clean)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside allowed base directory")
	}
	return clean, nil
}

func channelInfoTool() mcp.Tool {
	return mcp.NewTool("telegram_channel_info",
		mcp.WithDescription("Get info about this Telegram channel's capabilities"),
	)
}

func channelInfoHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"supports_attachment_delivery":true,"supports_followup_buttons":false,"status":"connected"}`), nil
	}
}

func sendAttachmentTool(allowPath bool) mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Send a file or image to the Telegram chat"),
		mcp.WithString("session_id", mcp.Description("Session ID (optional if only one session active)")),
		mcp.WithString("data_base64", mcp.Description("Base64-encoded file content")),
		mcp.WithString("name", mcp.Required(), mcp.Description("File name")),
		mcp.WithString("mime_type", mcp.Description("MIME type (optional)")),
	}
	if allowPath {
		opts = append(opts, mcp.WithString("path", mcp.Description("Absolute file path (only if allowed by server)")))
	}
	return mcp.NewTool("telegram_send_attachment", opts...)
}

const maxAttachmentSize = 50 * 1024 * 1024 // 50 MB

func makeSendAttachmentHandler(store *StateStore, token string, allowPath bool, baseDir string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// botOnce initializes the Telegram bot client lazily on first use.
	// If initialization fails (e.g. transient network error at startup), all
	// subsequent calls will return the same error for the lifetime of this handler.
	// This is acceptable because the MCP server process is short-lived and restarted
	// by the bot on each agent session. A persistent failure indicates a misconfiguration
	// (bad token) rather than a transient error.
	var (
		botOnce sync.Once
		bot     *telego.Bot
		botErr  error
	)
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID := req.GetString("session_id", "")
		dataB64 := req.GetString("data_base64", "")
		path := req.GetString("path", "")
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError("name is required: " + err.Error()), nil
		}
		mimeType := req.GetString("mime_type", "application/octet-stream")

		state, err := store.Read()
		if err != nil {
			return mcp.NewToolResultError("cannot read state: " + err.Error()), nil
		}

		chatID, err := ResolveChatID(state, sessionID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var data []byte
		if path != "" && allowPath {
			if baseDir == "" {
				return mcp.NewToolResultError("path mode requires ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR"), nil
			}
			clean, err := validatePathWithinBase(path, baseDir)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			data, err = os.ReadFile(clean)
			if err != nil {
				return mcp.NewToolResultError("cannot read file: " + err.Error()), nil
			}
		} else if dataB64 != "" {
			if len(dataB64) > maxAttachmentSize*4/3+4 {
				return mcp.NewToolResultError("data_base64 payload exceeds maximum allowed size"), nil
			}
			data, err = base64.StdEncoding.DecodeString(dataB64)
			if err != nil {
				return mcp.NewToolResultError("invalid base64: " + err.Error()), nil
			}
		} else {
			return mcp.NewToolResultError("data_base64 or path (when allowed) is required"), nil
		}
		if len(data) > maxAttachmentSize {
			return mcp.NewToolResultError("attachment exceeds maximum allowed size (50 MB)"), nil
		}

		botOnce.Do(func() { bot, botErr = telego.NewBot(token) })
		if botErr != nil {
			return mcp.NewToolResultError("bot init failed: " + botErr.Error()), nil
		}

		nr := &util.NamedReader{FileName: name, R: bytes.NewReader(data)}
		file := tu.File(nr)

		if strings.HasPrefix(mimeType, "image/") {
			_, err = bot.SendPhoto(ctx, &telego.SendPhotoParams{
				ChatID: tu.ID(chatID),
				Photo:  file,
			})
		} else {
			_, err = bot.SendDocument(ctx, &telego.SendDocumentParams{
				ChatID:   tu.ID(chatID),
				Document: file,
			})
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("send failed: %v", err)), nil
		}
		return mcp.NewToolResultText("sent"), nil
	}
}
