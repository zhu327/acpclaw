package commands

import (
	"context"
	"fmt"

	"github.com/zhu327/acpclaw/internal/domain"
)

// NewCommand handles /new.
type NewCommand struct {
	sessionMgr   domain.SessionManager
	defaultWs    string
	beforeSwitch func(ctx context.Context, chat domain.ChatRef)
}

// NewNewCommand creates a NewCommand.
func NewNewCommand(
	sm domain.SessionManager,
	defaultWs string,
	beforeSwitch func(ctx context.Context, chat domain.ChatRef),
) *NewCommand {
	return &NewCommand{sessionMgr: sm, defaultWs: defaultWs, beforeSwitch: beforeSwitch}
}

func (c *NewCommand) Name() string        { return "new" }
func (c *NewCommand) Description() string { return "Start a new session" }

func (c *NewCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	if c.beforeSwitch != nil {
		c.beforeSwitch(ctx, tc.Chat)
	}
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
