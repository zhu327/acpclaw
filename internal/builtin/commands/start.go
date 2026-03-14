package commands

import (
	"context"

	"github.com/zhu327/acpclaw/internal/domain"
)

// StartCommand handles /start.
type StartCommand struct{}

// NewStartCommand creates a StartCommand.
func NewStartCommand() *StartCommand {
	return &StartCommand{}
}

func (c *StartCommand) Name() string        { return "start" }
func (c *StartCommand) Description() string { return "Welcome message" }

func (c *StartCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	return &domain.Result{Text: "Welcome! Use /help for available commands."}, nil
}
