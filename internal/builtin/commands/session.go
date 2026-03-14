package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

// SessionCommand handles /session.
type SessionCommand struct {
	sessionMgr *AgentAdapter
}

// NewSessionCommand creates a SessionCommand.
func NewSessionCommand(sm *AgentAdapter) *SessionCommand {
	return &SessionCommand{sessionMgr: sm}
}

func (c *SessionCommand) Name() string        { return "session" }
func (c *SessionCommand) Description() string { return "List all sessions" }

func (c *SessionCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	sessions, err := c.sessionMgr.ListSessions(ctx, tc.Chat)
	if err != nil {
		if errors.Is(err, domain.ErrNoActiveProcess) {
			return &domain.Result{Text: "No active session. Use /new first."}, nil
		}
		return &domain.Result{Text: "❌ Failed to list sessions."}, nil
	}
	if len(sessions) == 0 {
		return &domain.Result{Text: "No sessions found."}, nil
	}
	activeID := ""
	if info := c.sessionMgr.ActiveSession(tc.Chat); info != nil {
		activeID = info.SessionID
	}
	var lines []string
	for i, s := range sessions {
		marker := ""
		if s.SessionID == activeID {
			marker = " (active)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s [%s]%s", i+1, sessionDisplayName(s), s.SessionID, marker))
	}
	return &domain.Result{Text: strings.Join(lines, "\n")}, nil
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
