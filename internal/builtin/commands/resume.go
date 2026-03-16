package commands

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zhu327/acpclaw/internal/domain"
)

// ResumeChoicesStore stores pending resume choices for callback resolution.
type ResumeChoicesStore interface {
	Set(chat domain.ChatRef, choices []domain.SessionInfo)
	Get(chat domain.ChatRef, index int) (*domain.SessionInfo, bool)
}

type resumeChoicesStore struct {
	mu      sync.Mutex
	choices map[string][]domain.SessionInfo
}

// NewResumeChoicesStore creates a new store.
func NewResumeChoicesStore() *resumeChoicesStore {
	return &resumeChoicesStore{choices: make(map[string][]domain.SessionInfo)}
}

func (s *resumeChoicesStore) Set(chat domain.ChatRef, choices []domain.SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.choices[chat.CompositeKey()] = choices
}

func (s *resumeChoicesStore) Get(chat domain.ChatRef, index int) (*domain.SessionInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	choices, ok := s.choices[chat.CompositeKey()]
	if !ok || index < 0 || index >= len(choices) {
		return nil, false
	}
	return &choices[index], true
}

// ResumeCommand handles /resume.
type ResumeCommand struct {
	sessionMgr domain.SessionManager
	store      ResumeChoicesStore
}

// NewResumeCommand creates a ResumeCommand.
func NewResumeCommand(sm domain.SessionManager, store ResumeChoicesStore) *ResumeCommand {
	return &ResumeCommand{sessionMgr: sm, store: store}
}

func (c *ResumeCommand) Name() string        { return "resume" }
func (c *ResumeCommand) Description() string { return "Resume a session" }

func (c *ResumeCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	sessions, err := c.sessionMgr.ListSessions(ctx, tc.Chat)
	if err != nil {
		if errors.Is(err, domain.ErrNoActiveProcess) {
			return &domain.Result{Text: "No active session. Use /new first."}, nil
		}
		return &domain.Result{Text: "❌ Failed to list sessions."}, nil
	}
	activeID := ""
	if info := c.sessionMgr.ActiveSession(tc.Chat); info != nil {
		activeID = info.SessionID
	}
	filtered := filterNonActiveSessions(sessions, activeID)
	if len(filtered) == 0 {
		return &domain.Result{Text: "No resumable sessions found."}, nil
	}

	if len(args) > 0 {
		return c.handleResumeByIndex(ctx, args, tc, filtered)
	}

	// No args: show inline keyboard for selection
	c.store.Set(tc.Chat, filtered)
	choices := make([]domain.SessionChoice, len(filtered))
	for i, s := range filtered {
		choices[i] = domain.SessionChoice{Index: i, DisplayName: sessionDisplayName(s)}
	}
	if err := tc.Responder.ShowResumeKeyboard(choices); err != nil {
		return &domain.Result{Text: "❌ Failed to show session list."}, nil
	}
	return &domain.Result{Text: "Pick a session to resume:", SuppressOutbound: true}, nil
}

func (c *ResumeCommand) handleResumeByIndex(
	ctx context.Context,
	args []string,
	tc *domain.TurnContext,
	filtered []domain.SessionInfo,
) (*domain.Result, error) {
	var n int
	if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil || n < 1 || n > len(filtered) {
		return &domain.Result{Text: "Invalid session number."}, nil
	}
	s := filtered[n-1]
	if err := c.sessionMgr.LoadSession(ctx, tc.Chat, s.SessionID, s.Workspace); err != nil {
		if errors.Is(err, domain.ErrLoadSessionNotSupported) {
			return &domain.Result{Text: "Session resume is not supported by the current agent."}, nil
		}
		if errors.Is(err, domain.ErrSessionNotFound) {
			return &domain.Result{Text: "Session expired or no longer available."}, nil
		}
		return &domain.Result{Text: "❌ Failed to resume session."}, nil
	}
	return &domain.Result{Text: fmt.Sprintf("Session resumed: `%s` in `%s`", s.SessionID, s.Workspace)}, nil
}

func filterNonActiveSessions(sessions []domain.SessionInfo, activeID string) []domain.SessionInfo {
	var out []domain.SessionInfo
	for _, s := range sessions {
		if s.SessionID != activeID {
			out = append(out, s)
		}
	}
	return out
}
