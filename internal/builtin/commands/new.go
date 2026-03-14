package commands

import (
	"context"
	"fmt"

	"github.com/zhu327/acpclaw/internal/domain"
)

// NewCommand handles /new.
type NewCommand struct {
	sessionMgr *AgentAdapter
	defaultWs  string
}

// NewNewCommand creates a NewCommand.
func NewNewCommand(sm *AgentAdapter, defaultWs string) *NewCommand {
	return &NewCommand{sessionMgr: sm, defaultWs: defaultWs}
}

func (c *NewCommand) Name() string        { return "new" }
func (c *NewCommand) Description() string { return "Start a new session" }

func (c *NewCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	workspace := resolveWorkspace(args, c.defaultWs)
	if err := c.sessionMgr.NewSession(ctx, tc.Chat, workspace); err != nil {
		return &domain.Result{Text: "❌ Failed to start session."}, nil
	}
	info := c.sessionMgr.ActiveSession(tc.Chat)
	if info != nil {
		return &domain.Result{Text: fmt.Sprintf("Session started: `%s` in `%s`", info.SessionID, info.Workspace)}, nil
	}
	return &domain.Result{Text: "Session started."}, nil
}
