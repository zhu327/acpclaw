package agent

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/domain"
)

// slogWriter adapts agent subprocess stderr to slog.
type slogWriter struct {
	level slog.Level
	msg   string
}

func (w *slogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		slog.Log(context.Background(), w.level, w.msg, "output", line)
	}
	return len(p), nil
}

// BuildContentBlocks converts domain.PromptInput to SDK ContentBlock slice.
func BuildContentBlocks(input domain.PromptInput) []acpsdk.ContentBlock {
	var blocks []acpsdk.ContentBlock
	if input.Text != "" {
		blocks = append(blocks, acpsdk.TextBlock(input.Text))
	}
	for _, img := range input.Images {
		data := base64.StdEncoding.EncodeToString(img.Data)
		blocks = append(blocks, acpsdk.ImageBlock(data, img.MIMEType))
	}
	for _, f := range input.Files {
		name := f.Name
		if name == "" {
			name = "attachment.bin"
		}
		if f.TextContent != nil {
			// Text file semantic (Python parity): File: <name>\n\n<content>
			payload := "File: " + name + "\n\n" + *f.TextContent
			blocks = append(blocks, acpsdk.TextBlock(payload))
			continue
		}
		// Binary file semantic (Python parity): Binary file attached: <name> (<mime>)
		mime := f.MIMEType
		if mime == "" {
			mime = "unknown"
		}
		payload := "Binary file attached: " + name + " (" + mime + ")"
		blocks = append(blocks, acpsdk.TextBlock(payload))
	}
	return blocks
}

// logTextPreview returns a collapsed, truncated preview of text for log output
func logTextPreview(text string, maxLen int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return "<empty>"
	}
	if len(collapsed) <= maxLen {
		return collapsed
	}
	return collapsed[:maxLen] + "..."
}
