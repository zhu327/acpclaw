package domain

import "context"

// Command represents a user-invocable slash command (e.g. /new, /help).
type Command interface {
	Name() string
	Description() string
	Execute(ctx context.Context, args []string, tc *TurnContext) (*Result, error)
}
