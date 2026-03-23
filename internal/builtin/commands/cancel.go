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
	cancelAndDrain func(ctx context.Context, chat domain.ChatRef) (drained int, err error)
}

// NewCancelCommand creates a CancelCommand. cancelAndDrain atomically flags cancel, drains queued
// prompts for the chat, then cancels the running agent prompt (if any). May be nil for tests.
func NewCancelCommand(
	cancelAndDrain func(ctx context.Context, chat domain.ChatRef) (drained int, err error),
) *CancelCommand {
	return &CancelCommand{cancelAndDrain: cancelAndDrain}
}

func (c *CancelCommand) Name() string { return "cancel" }
func (c *CancelCommand) Description() string {
	return "Cancel current prompt and clear queued messages"
}

func (c *CancelCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	n := 0
	var err error
	if c.cancelAndDrain != nil {
		n, err = c.cancelAndDrain(ctx, tc.Chat)
	}
	if err == nil {
		if n > 0 {
			return &domain.Result{
				Text: fmt.Sprintf("Cancelled the current operation and cleared %d queued message(s).", n),
			}, nil
		}
		return &domain.Result{Text: "Cancelled current operation."}, nil
	}
	if errors.Is(err, domain.ErrNoActiveSession) {
		if n > 0 {
			return &domain.Result{
				Text: fmt.Sprintf("Cleared %d queued message(s). No active agent session to cancel.", n),
			}, nil
		}
		return &domain.Result{Text: "No active session. Use /new first."}, nil
	}
	slog.Error("cancel prompt failed", "chat", tc.Chat.CompositeKey(), "error", err)
	if n > 0 {
		return &domain.Result{
			Text: fmt.Sprintf("Cleared %d queued message(s), but could not cancel the running task. Try again.", n),
		}, nil
	}
	return &domain.Result{Text: "❌ Failed to cancel current task."}, nil
}
