package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/zhu327/acpclaw/internal/acp"
	"github.com/zhu327/acpclaw/internal/channel"
)

var commandSet = map[string]bool{
	"start": true, "help": true, "new": true,
	"session": true, "resume": true,
	"cancel": true, "reconnect": true,
	"status": true,
}

func parseCommand(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	end := strings.IndexByte(trimmed, ' ')
	var name string
	if end == -1 {
		name = strings.ToLower(trimmed[1:])
	} else {
		name = strings.ToLower(trimmed[1:end])
	}
	if commandSet[name] {
		return name
	}
	return ""
}

func parseCommandArgs(text string) []string {
	trimmed := strings.TrimSpace(text)
	idx := strings.IndexByte(trimmed, ' ')
	if idx == -1 {
		return nil
	}
	rest := strings.TrimSpace(trimmed[idx+1:])
	if rest == "" {
		return nil
	}
	return strings.Fields(rest)
}

const helpText = `ACP-Claw Bot

Session Management
/new [workspace]  — Start a new session
/session  — List all sessions
/resume [N]  — Resume a session

Controls
/cancel  — Cancel current prompt
/reconnect  — Reconnect ACP process

/status  — Show status
/help  — Show this help`

func (d *Dispatcher) execCommand(cmd string, msg channel.InboundMessage, resp channel.Responder) {
	chatID, ok := parseChatID(msg.ChatID)
	if !ok {
		resp.Reply(channel.OutboundMessage{Text: "Invalid chat ID."}) //nolint:errcheck
		return
	}
	args := parseCommandArgs(msg.Text)
	ctx := context.Background()

	switch cmd {
	case "start":
		resp.Reply(channel.OutboundMessage{Text: "Welcome! Use /help for available commands."}) //nolint:errcheck

	case "help":
		resp.Reply(channel.OutboundMessage{Text: helpText}) //nolint:errcheck

	case "new":
		d.handleNew(ctx, chatID, args, resp)

	case "session":
		d.handleSession(ctx, chatID, resp)

	case "resume":
		d.handleResume(ctx, chatID, args, msg, resp)

	case "cancel":
		d.handleCancel(ctx, chatID, resp)

	case "reconnect":
		d.handleReconnect(ctx, chatID, args, resp)

	case "status":
		d.handleStatus(chatID, resp)

	default:
		resp.Reply(channel.OutboundMessage{Text: "Unknown command. Use /help."}) //nolint:errcheck
	}
}

// ensureAgentConfigured 检查 agent 是否已配置，未配置时发送提示并返回 false
func (d *Dispatcher) ensureAgentConfigured(resp channel.Responder) bool {
	if d.agentSvc != nil {
		return true
	}
	resp.Reply(channel.OutboundMessage{Text: "Agent not configured."}) //nolint:errcheck
	return false
}

// handleListSessionsError 处理 ListSessions 错误，有错误时发送提示并返回 false
func (d *Dispatcher) handleListSessionsError(resp channel.Responder, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, acp.ErrNoActiveProcess) {
		resp.Reply(channel.OutboundMessage{Text: "No active session. Use /new first."}) //nolint:errcheck
	} else {
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to list sessions."}) //nolint:errcheck
	}
	return false
}

func (d *Dispatcher) replySessionStarted(resp channel.Responder, chatID int64) {
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		resp.Reply(channel.OutboundMessage{ //nolint:errcheck
			Text: fmt.Sprintf("Session started: `%s` in `%s`", info.SessionID, info.Workspace),
		})
	} else {
		resp.Reply(channel.OutboundMessage{Text: "Session started."}) //nolint:errcheck
	}
}

func (d *Dispatcher) resolveWorkspace(args []string) string {
	ws := strings.TrimSpace(strings.Join(args, " "))
	if ws == "" {
		if d.cfg.DefaultWorkspace != "" {
			return d.cfg.DefaultWorkspace
		}
		return "."
	}
	return ws
}

func (d *Dispatcher) handleNew(ctx context.Context, chatID int64, args []string, resp channel.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if d.cfg.AutoSummarize && d.memorySvc != nil {
		chatIDStr := strconv.FormatInt(chatID, 10)
		summarizer := acp.NewAgentSummarizer(d.agentSvc, chatID)
		if err := d.memorySvc.SummarizeSession(ctx, chatIDStr, summarizer); err != nil {
			slog.Warn("failed to summarize session", "chat_id", chatID, "error", err)
		}
	}

	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.NewSession(ctx, chatID, workspace); err != nil {
		slog.Error("failed to start session", "chat_id", chatID, "error", err)
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to start session."}) //nolint:errcheck
		return
	}
	d.replySessionStarted(resp, chatID)
}

func (d *Dispatcher) handleSession(ctx context.Context, chatID int64, resp channel.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	sessions, err := d.agentSvc.ListSessions(ctx, chatID)
	if !d.handleListSessionsError(resp, err) {
		return
	}
	if len(sessions) == 0 {
		resp.Reply(channel.OutboundMessage{Text: "No sessions found."}) //nolint:errcheck
		return
	}
	activeID := d.activeSessionID(chatID)
	var lines []string
	for i, s := range sessions {
		marker := ""
		if s.SessionID == activeID {
			marker = " (active)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s [%s]%s", i+1, sessionDisplayName(s), s.SessionID, marker))
	}
	resp.Reply(channel.OutboundMessage{Text: strings.Join(lines, "\n")}) //nolint:errcheck
}

func (d *Dispatcher) handleResume(
	ctx context.Context, chatID int64, args []string, msg channel.InboundMessage, resp channel.Responder,
) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	sessions, err := d.agentSvc.ListSessions(ctx, chatID)
	if !d.handleListSessionsError(resp, err) {
		return
	}
	filtered := filterNonActiveSessions(sessions, d.activeSessionID(chatID))
	if len(filtered) == 0 {
		resp.Reply(channel.OutboundMessage{Text: "No resumable sessions found."}) //nolint:errcheck
		return
	}

	if len(args) > 0 {
		d.handleResumeByIndex(ctx, chatID, args, resp, filtered)
		return
	}

	// No args: show inline keyboard for selection
	d.resumeChoicesMu.Lock()
	d.pendingResumeChoices[chatID] = filtered
	d.resumeChoicesMu.Unlock()

	choices := make([]channel.SessionChoice, len(filtered))
	for i, s := range filtered {
		choices[i] = channel.SessionChoice{Index: i, DisplayName: sessionDisplayName(s)}
	}
	resp.ShowResumeKeyboard(choices) //nolint:errcheck
}

func sessionDisplayName(s acp.SessionInfo) string {
	if s.Title != "" {
		return s.Title
	}
	if s.Workspace != "" {
		return s.Workspace
	}
	return s.SessionID
}

// activeSessionID 返回当前活跃会话 ID，无活跃会话时返回空字符串
func (d *Dispatcher) activeSessionID(chatID int64) string {
	if active := d.agentSvc.ActiveSession(chatID); active != nil {
		return active.SessionID
	}
	return ""
}

func filterNonActiveSessions(sessions []acp.SessionInfo, activeID string) []acp.SessionInfo {
	var out []acp.SessionInfo
	for _, s := range sessions {
		if s.SessionID != activeID {
			out = append(out, s)
		}
	}
	return out
}

// handleResumeByIndex 处理带索引的 /resume 命令
func (d *Dispatcher) handleResumeByIndex(
	ctx context.Context, chatID int64, args []string, resp channel.Responder, filtered []acp.SessionInfo,
) {
	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || n > len(filtered) {
		resp.Reply(channel.OutboundMessage{Text: "Invalid session number."}) //nolint:errcheck
		return
	}
	s := filtered[n-1]
	if err := d.agentSvc.LoadSession(ctx, chatID, s.SessionID, s.Workspace); err != nil {
		if errors.Is(err, acp.ErrLoadSessionNotSupported) {
			_ = resp.Reply(channel.OutboundMessage{Text: "Session resume is not supported by the current agent."})
			return
		}
		if errors.Is(err, acp.ErrSessionNotFound) {
			resp.Reply(channel.OutboundMessage{Text: "Session expired or no longer available."}) //nolint:errcheck
			return
		}
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to resume session."}) //nolint:errcheck
		return
	}
	resp.Reply(channel.OutboundMessage{ //nolint:errcheck
		Text: fmt.Sprintf("Session resumed: `%s` in `%s`", s.SessionID, s.Workspace),
	})
}

func (d *Dispatcher) handleCancel(ctx context.Context, chatID int64, resp channel.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if err := d.agentSvc.Cancel(ctx, chatID); err != nil {
		if errors.Is(err, acp.ErrNoActiveSession) {
			resp.Reply(channel.OutboundMessage{Text: "No active session. Use /new first."}) //nolint:errcheck
			return
		}
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to cancel current task."}) //nolint:errcheck
		return
	}
	resp.Reply(channel.OutboundMessage{Text: "Cancelled current operation."}) //nolint:errcheck
}

func (d *Dispatcher) handleReconnect(ctx context.Context, chatID int64, args []string, resp channel.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.Reconnect(ctx, chatID, workspace); err != nil {
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to reconnect."}) //nolint:errcheck
		return
	}
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		resp.Reply(channel.OutboundMessage{ //nolint:errcheck
			Text: fmt.Sprintf("ACP process reconnected. New session: `%s` in `%s`", info.SessionID, info.Workspace),
		})
	} else {
		resp.Reply(channel.OutboundMessage{Text: "ACP process reconnected."}) //nolint:errcheck
	}
}

func (d *Dispatcher) handleStatus(chatID int64, resp channel.Responder) {
	lines := []string{"**Status**"}
	if d.agentSvc != nil {
		if info := d.agentSvc.ActiveSession(chatID); info != nil {
			lines = append(lines, fmt.Sprintf("- Session: %s", info.SessionID))
			lines = append(lines, fmt.Sprintf("- Workspace: %s", info.Workspace))
		} else {
			lines = append(lines, "- No active session")
		}
	} else {
		lines = append(lines, "- Agent not configured")
	}
	resp.Reply(channel.OutboundMessage{Text: strings.Join(lines, "\n")}) //nolint:errcheck
}
