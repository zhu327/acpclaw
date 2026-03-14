package commands

import (
	"log/slog"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

func replyText(resp domain.Replier, text string) {
	if err := resp.Reply(domain.OutboundMessage{Text: text}); err != nil {
		slog.Debug("reply failed (best effort)", "error", err)
	}
}

func resolveWorkspace(args []string, defaultWs string) string {
	ws := strings.TrimSpace(strings.Join(args, " "))
	if ws == "" {
		if defaultWs != "" {
			return defaultWs
		}
		return "."
	}
	return ws
}
