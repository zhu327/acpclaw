package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/zhu327/acpclaw/internal/agent"
	"github.com/zhu327/acpclaw/internal/domain"
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

func (d *Dispatcher) execCommand(cmd string, msg domain.InboundMessage, resp domain.Responder) {
	chatID, ok := parseChatID(msg.ChatID)
	if !ok {
		resp.Reply(domain.OutboundMessage{Text: "Invalid chat ID."}) //nolint:errcheck
		return
	}
	args := parseCommandArgs(msg.Text)
	ctx := context.Background()

	switch cmd {
	case "start":
		resp.Reply(domain.OutboundMessage{Text: "Welcome! Use /help for available commands."}) //nolint:errcheck

	case "help":
		resp.Reply(domain.OutboundMessage{Text: helpText}) //nolint:errcheck

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
		resp.Reply(domain.OutboundMessage{Text: "Unknown command. Use /help."}) //nolint:errcheck
	}
}

// ensureAgentConfigured checks whether the agent is configured; if not, it replies and returns false.
func (d *Dispatcher) ensureAgentConfigured(resp domain.Responder) bool {
	if d.agentSvc != nil {
		return true
	}
	resp.Reply(domain.OutboundMessage{Text: "Agent not configured."}) //nolint:errcheck
	return false
}

// handleListSessionsError handles ListSessions errors; on error it replies and returns false.
func (d *Dispatcher) handleListSessionsError(resp domain.Responder, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, agent.ErrNoActiveProcess) {
		resp.Reply(domain.OutboundMessage{Text: "No active session. Use /new first."}) //nolint:errcheck
	} else {
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to list sessions."}) //nolint:errcheck
	}
	return false
}

func (d *Dispatcher) replySessionStarted(resp domain.Responder, chatID int64) {
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		resp.Reply(domain.OutboundMessage{ //nolint:errcheck
			Text: fmt.Sprintf("Session started: `%s` in `%s`", info.SessionID, info.Workspace),
		})
	} else {
		resp.Reply(domain.OutboundMessage{Text: "Session started."}) //nolint:errcheck
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

func (d *Dispatcher) handleNew(ctx context.Context, chatID int64, args []string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if d.cfg.AutoSummarize && d.memorySvc != nil {
		chatIDStr := strconv.FormatInt(chatID, 10)
		summarizer := agent.NewAgentSummarizer(d.agentSvc, chatID)
		if err := d.memorySvc.SummarizeSession(ctx, chatIDStr, summarizer); err != nil {
			slog.Warn("failed to summarize session", "chat_id", chatID, "error", err)
		}
	}

	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.NewSession(ctx, chatID, workspace); err != nil {
		slog.Error("failed to start session", "chat_id", chatID, "error", err)
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to start session."}) //nolint:errcheck
		return
	}
	d.replySessionStarted(resp, chatID)
}

func (d *Dispatcher) handleSession(ctx context.Context, chatID int64, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	sessions, err := d.agentSvc.ListSessions(ctx, chatID)
	if !d.handleListSessionsError(resp, err) {
		return
	}
	if len(sessions) == 0 {
		resp.Reply(domain.OutboundMessage{Text: "No sessions found."}) //nolint:errcheck
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
	resp.Reply(domain.OutboundMessage{Text: strings.Join(lines, "\n")}) //nolint:errcheck
}

func (d *Dispatcher) handleResume(
	ctx context.Context, chatID int64, args []string, msg domain.InboundMessage, resp domain.Responder,
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
		resp.Reply(domain.OutboundMessage{Text: "No resumable sessions found."}) //nolint:errcheck
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

	choices := make([]domain.SessionChoice, len(filtered))
	for i, s := range filtered {
		choices[i] = domain.SessionChoice{Index: i, DisplayName: sessionDisplayName(s)}
	}
	resp.ShowResumeKeyboard(choices) //nolint:errcheck
}

func sessionDisplayName(s domain.SessionInfo) string {
	if s.Title != "" {
		return s.Title
	}
	if s.Workspace != "" {
		return s.Workspace
	}
	return s.SessionID
}

// activeSessionID returns the current active session ID, or empty string if none.
func (d *Dispatcher) activeSessionID(chatID int64) string {
	if active := d.agentSvc.ActiveSession(chatID); active != nil {
		return active.SessionID
	}
	return ""
}

func filterNonActiveSessions(sessions []domain.SessionInfo, activeID string) []domain.SessionInfo {
	var out []domain.SessionInfo
	for _, s := range sessions {
		if s.SessionID != activeID {
			out = append(out, s)
		}
	}
	return out
}

// handleResumeByIndex handles /resume with an index.
func (d *Dispatcher) handleResumeByIndex(
	ctx context.Context, chatID int64, args []string, resp domain.Responder, filtered []domain.SessionInfo,
) {
	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || n > len(filtered) {
		resp.Reply(domain.OutboundMessage{Text: "Invalid session number."}) //nolint:errcheck
		return
	}
	s := filtered[n-1]
	if err := d.agentSvc.LoadSession(ctx, chatID, s.SessionID, s.Workspace); err != nil {
		if errors.Is(err, agent.ErrLoadSessionNotSupported) {
			_ = resp.Reply(domain.OutboundMessage{Text: "Session resume is not supported by the current agent."})
			return
		}
		if errors.Is(err, agent.ErrSessionNotFound) {
			resp.Reply(domain.OutboundMessage{Text: "Session expired or no longer available."}) //nolint:errcheck
			return
		}
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to resume session."}) //nolint:errcheck
		return
	}
	resp.Reply(domain.OutboundMessage{ //nolint:errcheck
		Text: fmt.Sprintf("Session resumed: `%s` in `%s`", s.SessionID, s.Workspace),
	})
}

func (d *Dispatcher) handleCancel(ctx context.Context, chatID int64, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if err := d.agentSvc.Cancel(ctx, chatID); err != nil {
		if errors.Is(err, agent.ErrNoActiveSession) {
			resp.Reply(domain.OutboundMessage{Text: "No active session. Use /new first."}) //nolint:errcheck
			return
		}
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to cancel current task."}) //nolint:errcheck
		return
	}
	resp.Reply(domain.OutboundMessage{Text: "Cancelled current operation."}) //nolint:errcheck
}

func (d *Dispatcher) handleReconnect(ctx context.Context, chatID int64, args []string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.Reconnect(ctx, chatID, workspace); err != nil {
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to reconnect."}) //nolint:errcheck
		return
	}
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		resp.Reply(domain.OutboundMessage{ //nolint:errcheck
			Text: fmt.Sprintf("ACP process reconnected. New session: `%s` in `%s`", info.SessionID, info.Workspace),
		})
	} else {
		resp.Reply(domain.OutboundMessage{Text: "ACP process reconnected."}) //nolint:errcheck
	}
}

func (d *Dispatcher) handleStatus(chatID int64, resp domain.Responder) {
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
	resp.Reply(domain.OutboundMessage{Text: strings.Join(lines, "\n")}) //nolint:errcheck
}
