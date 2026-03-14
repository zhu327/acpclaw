package commands

import (
	"context"

	"github.com/zhu327/acpclaw/internal/domain"
)

// AgentAdapter adapts domain.AgentService to SessionManager and Prompter.
type AgentAdapter struct {
	svc domain.AgentService
}

// NewAgentAdapter creates an adapter from AgentService.
func NewAgentAdapter(svc domain.AgentService) *AgentAdapter {
	return &AgentAdapter{svc: svc}
}

func (a *AgentAdapter) chatID(chat domain.ChatRef) string {
	return chat.CompositeKey()
}

func (a *AgentAdapter) NewSession(ctx context.Context, chat domain.ChatRef, workspace string) error {
	return a.svc.NewSession(ctx, a.chatID(chat), workspace)
}

func (a *AgentAdapter) LoadSession(ctx context.Context, chat domain.ChatRef, sessionID, workspace string) error {
	return a.svc.LoadSession(ctx, a.chatID(chat), sessionID, workspace)
}

func (a *AgentAdapter) ListSessions(ctx context.Context, chat domain.ChatRef) ([]domain.SessionInfo, error) {
	return a.svc.ListSessions(ctx, a.chatID(chat))
}

func (a *AgentAdapter) ActiveSession(chat domain.ChatRef) *domain.SessionInfo {
	return a.svc.ActiveSession(a.chatID(chat))
}

func (a *AgentAdapter) Reconnect(ctx context.Context, chat domain.ChatRef, workspace string) error {
	return a.svc.Reconnect(ctx, a.chatID(chat), workspace)
}

func (a *AgentAdapter) Shutdown() {
	a.svc.Shutdown()
}

func (a *AgentAdapter) Prompt(ctx context.Context, chat domain.ChatRef, input domain.PromptInput) (*domain.AgentReply, error) {
	return a.svc.Prompt(ctx, a.chatID(chat), input)
}

func (a *AgentAdapter) Cancel(ctx context.Context, chat domain.ChatRef) error {
	return a.svc.Cancel(ctx, a.chatID(chat))
}
