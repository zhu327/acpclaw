package commands

import (
	"context"
	"sort"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

const (
	helpHeader = `ACP-Claw Bot

Session Management
`
	helpFallback = helpHeader + "/new [workspace]  — Start a new session\n/session  — List all sessions\n/resume [N]  — Resume a session\n\nControls\n/cancel  — Cancel current prompt\n/reconnect  — Reconnect ACP process\n\n/status  — Show status\n/help  — Show this help"
)

// HelpCommand handles /help.
type HelpCommand struct{}

// NewHelpCommand creates a HelpCommand.
func NewHelpCommand() *HelpCommand {
	return &HelpCommand{}
}

func (c *HelpCommand) Name() string        { return "help" }
func (c *HelpCommand) Description() string { return "Show help" }

func (c *HelpCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	cmds, _ := tc.State["commands"].(map[string]domain.Command)
	if cmds == nil {
		return &domain.Result{Text: helpFallback}, nil
	}
	var names []string
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		cmd := cmds[name]
		lines = append(lines, "/"+name+"  — "+cmd.Description())
	}
	return &domain.Result{Text: helpHeader + strings.Join(lines, "\n")}, nil
}
