package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zhu327/acpclaw/internal/domain"
)

// CancelCommand handles /cancel.
type CancelCommand struct {
	prompter domain.Prompter
	drain    func(chat domain.ChatRef) int
}

// NewCancelCommand creates a CancelCommand. drain may be nil; it removes queued prompts not yet started.
func NewCancelCommand(p domain.Prompter, drain func(chat domain.ChatRef) int) *CancelCommand {
	return &CancelCommand{prompter: p, drain: drain}
}

func (c *CancelCommand) Name() string { return "cancel" }
func (c *CancelCommand) Description() string {
	return "Cancel current prompt and clear queued messages"
}

func (c *CancelCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	n := 0
	if c.drain != nil {
		n = c.drain(tc.Chat)
	}
	err := c.prompter.Cancel(ctx, tc.Chat)
	if err == nil {
		if n > 0 {
			return &domain.Result{Text: fmt.Sprintf("Cancelled the current operation and cleared %d queued message(s).", n)}, nil
		}
		return &domain.Result{Text: "Cancelled current operation."}, nil
	}
	if errors.Is(err, domain.ErrNoActiveSession) {
		if n > 0 {
			return &domain.Result{Text: fmt.Sprintf("Cleared %d queued message(s). No active agent session to cancel.", n)}, nil
		}
		return &domain.Result{Text: "No active session. Use /new first."}, nil
	}
	slog.Error("cancel prompt failed", "chat", tc.Chat.CompositeKey(), "error", err)
	if n > 0 {
		return &domain.Result{Text: fmt.Sprintf("Cleared %d queued message(s), but could not cancel the running task. Try again.", n)}, nil
	}
	return &domain.Result{Text: "❌ Failed to cancel current task."}, nil
}
