package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

func replyBestEffort(resp domain.Replier, text string) {
	_ = resp.Reply(domain.OutboundMessage{Text: text})
}

func (d *Dispatcher) execCommand(cmd string, msg domain.InboundMessage, resp domain.Responder) {
	chatID := msg.ChatID
	if chatID == "" {
		replyBestEffort(resp, "Invalid chat ID.")
		return
	}
	args := parseCommandArgs(msg.Text)
	ctx := context.Background()

	switch cmd {
	case "start":
		replyBestEffort(resp, "Welcome! Use /help for available commands.")

	case "help":
		replyBestEffort(resp, helpText)

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
		replyBestEffort(resp, "Unknown command. Use /help.")
	}
}

// ensureAgentConfigured checks whether the agent is configured; if not, it replies and returns false.
func (d *Dispatcher) ensureAgentConfigured(resp domain.Responder) bool {
	if d.agentSvc != nil {
		return true
	}
	replyBestEffort(resp, "Agent not configured.")
	return false
}

// handleListSessionsError handles ListSessions errors; on error it replies and returns false.
func (d *Dispatcher) handleListSessionsError(resp domain.Responder, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, domain.ErrNoActiveProcess) {
		replyBestEffort(resp, "No active session. Use /new first.")
	} else {
		replyBestEffort(resp, "❌ Failed to list sessions.")
	}
	return false
}

func (d *Dispatcher) replySessionStarted(resp domain.Responder, chatID string) {
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		replyBestEffort(resp, fmt.Sprintf("Session started: `%s` in `%s`", info.SessionID, info.Workspace))
	} else {
		replyBestEffort(resp, "Session started.")
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

func (d *Dispatcher) handleNew(ctx context.Context, chatID string, args []string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if d.cfg.AutoSummarize && d.memorySvc != nil && d.cfg.NewSummarizer != nil {
		summarizer := d.cfg.NewSummarizer(chatID)
		if err := d.memorySvc.SummarizeSession(ctx, chatID, summarizer); err != nil {
			slog.Warn("failed to summarize session", "chat_id", chatID, "error", err)
		}
	}

	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.NewSession(ctx, chatID, workspace); err != nil {
		slog.Error("failed to start session", "chat_id", chatID, "error", err)
		replyBestEffort(resp, "❌ Failed to start session.")
		return
	}
	d.replySessionStarted(resp, chatID)
}

func (d *Dispatcher) handleSession(ctx context.Context, chatID string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	sessions, err := d.agentSvc.ListSessions(ctx, chatID)
	if !d.handleListSessionsError(resp, err) {
		return
	}
	if len(sessions) == 0 {
		replyBestEffort(resp, "No sessions found.")
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
	replyBestEffort(resp, strings.Join(lines, "\n"))
}

func (d *Dispatcher) handleResume(
	ctx context.Context, chatID string, args []string, msg domain.InboundMessage, resp domain.Responder,
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
		replyBestEffort(resp, "No resumable sessions found.")
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
func (d *Dispatcher) activeSessionID(chatID string) string {
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
	ctx context.Context, chatID string, args []string, resp domain.Responder, filtered []domain.SessionInfo,
) {
	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || n > len(filtered) {
		replyBestEffort(resp, "Invalid session number.")
		return
	}
	s := filtered[n-1]
	if err := d.agentSvc.LoadSession(ctx, chatID, s.SessionID, s.Workspace); err != nil {
		if errors.Is(err, domain.ErrLoadSessionNotSupported) {
			replyBestEffort(resp, "Session resume is not supported by the current agent.")
			return
		}
		if errors.Is(err, domain.ErrSessionNotFound) {
			replyBestEffort(resp, "Session expired or no longer available.")
			return
		}
		replyBestEffort(resp, "❌ Failed to resume session.")
		return
	}
	replyBestEffort(resp, fmt.Sprintf("Session resumed: `%s` in `%s`", s.SessionID, s.Workspace))
}

func (d *Dispatcher) handleCancel(ctx context.Context, chatID string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	if err := d.agentSvc.Cancel(ctx, chatID); err != nil {
		if errors.Is(err, domain.ErrNoActiveSession) {
			replyBestEffort(resp, "No active session. Use /new first.")
			return
		}
		replyBestEffort(resp, "❌ Failed to cancel current task.")
		return
	}
	replyBestEffort(resp, "Cancelled current operation.")
}

func (d *Dispatcher) handleReconnect(ctx context.Context, chatID string, args []string, resp domain.Responder) {
	if !d.ensureAgentConfigured(resp) {
		return
	}
	workspace := d.resolveWorkspace(args)
	if err := d.agentSvc.Reconnect(ctx, chatID, workspace); err != nil {
		replyBestEffort(resp, "❌ Failed to reconnect.")
		return
	}
	info := d.agentSvc.ActiveSession(chatID)
	if info != nil {
		replyBestEffort(
			resp,
			fmt.Sprintf("ACP process reconnected. New session: `%s` in `%s`", info.SessionID, info.Workspace),
		)
	} else {
		replyBestEffort(resp, "ACP process reconnected.")
	}
}

func (d *Dispatcher) handleStatus(chatID string, resp domain.Responder) {
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
	replyBestEffort(resp, strings.Join(lines, "\n"))
}
