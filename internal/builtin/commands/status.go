package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

// StatusCommand handles /status.
type StatusCommand struct {
	sessionMgr domain.SessionManager
}

// NewStatusCommand creates a StatusCommand.
func NewStatusCommand(sm domain.SessionManager) *StatusCommand {
	return &StatusCommand{sessionMgr: sm}
}

func (c *StatusCommand) Name() string        { return "status" }
func (c *StatusCommand) Description() string { return "Show status" }

func (c *StatusCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	lines := []string{"**Status**"}
	if c.sessionMgr != nil {
		if info := c.sessionMgr.ActiveSession(tc.Chat); info != nil {
			lines = append(lines, fmt.Sprintf("- Session: %s", info.SessionID))
			lines = append(lines, fmt.Sprintf("- Workspace: %s", info.Workspace))
		} else {
			lines = append(lines, "- No active session")
		}
	} else {
		lines = append(lines, "- Agent not configured")
	}
	return &domain.Result{Text: strings.Join(lines, "\n")}, nil
}
