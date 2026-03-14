package commands

import (
	"context"
	"errors"

	"github.com/zhu327/acpclaw/internal/domain"
)

// CancelCommand handles /cancel.
type CancelCommand struct {
	prompter domain.Prompter
}

// NewCancelCommand creates a CancelCommand.
func NewCancelCommand(p domain.Prompter) *CancelCommand {
	return &CancelCommand{prompter: p}
}

func (c *CancelCommand) Name() string        { return "cancel" }
func (c *CancelCommand) Description() string { return "Cancel current prompt" }

func (c *CancelCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	if err := c.prompter.Cancel(ctx, tc.Chat); err != nil {
		if errors.Is(err, domain.ErrNoActiveSession) {
			return &domain.Result{Text: "No active session. Use /new first."}, nil
		}
		return &domain.Result{Text: "❌ Failed to cancel current task."}, nil
	}
	return &domain.Result{Text: "Cancelled current operation."}, nil
}
