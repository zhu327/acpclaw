package commands

import (
	"context"
	"fmt"

	"github.com/zhu327/acpclaw/internal/domain"
)

// ReconnectCommand handles /reconnect.
type ReconnectCommand struct {
	sessionMgr   domain.SessionManager
	defaultWs    string
	beforeSwitch func(ctx context.Context, chat domain.ChatRef)
}

// NewReconnectCommand creates a ReconnectCommand.
func NewReconnectCommand(
	sm domain.SessionManager,
	defaultWs string,
	beforeSwitch func(ctx context.Context, chat domain.ChatRef),
) *ReconnectCommand {
	return &ReconnectCommand{sessionMgr: sm, defaultWs: defaultWs, beforeSwitch: beforeSwitch}
}

func (c *ReconnectCommand) Name() string        { return "reconnect" }
func (c *ReconnectCommand) Description() string { return "Reconnect ACP process" }

func (c *ReconnectCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	if c.beforeSwitch != nil {
		c.beforeSwitch(ctx, tc.Chat)
	}
	workspace := resolveWorkspace(args, c.defaultWs)
	if err := c.sessionMgr.Reconnect(ctx, tc.Chat, workspace); err != nil {
		return &domain.Result{Text: "❌ Failed to reconnect."}, nil
	}
	info := c.sessionMgr.ActiveSession(tc.Chat)
	if info != nil {
		return &domain.Result{
			Text: fmt.Sprintf("ACP process reconnected. New session: `%s` in `%s`", info.SessionID, info.Workspace),
		}, nil
	}
	return &domain.Result{Text: "ACP process reconnected."}, nil
}
