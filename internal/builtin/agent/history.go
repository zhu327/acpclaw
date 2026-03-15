package agent

import (
	"context"
	"log/slog"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/domain"
)

const maxSessionHistory = 20

func (s *AcpAgentService) detachLiveSession(chatID string) *liveSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveByChat[chatID]
	if live == nil {
		return nil
	}
	delete(s.liveByChat, chatID)
	return live
}

func upsertCappedSessionHistory(history []domain.SessionInfo, info domain.SessionInfo) []domain.SessionInfo {
	filtered := make([]domain.SessionInfo, 0, len(history)+1)
	for _, h := range history {
		if h.SessionID != info.SessionID {
			filtered = append(filtered, h)
		}
	}
	filtered = append(filtered, info)
	if len(filtered) > maxSessionHistory {
		filtered = filtered[len(filtered)-maxSessionHistory:]
	}
	return filtered
}

func (s *AcpAgentService) attachSession(chatID string, live *liveSession) {
	s.mu.Lock()
	s.liveByChat[chatID] = live
	s.mu.Unlock()
}

func (s *AcpAgentService) removeSessionFromHistory(chatID string, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.sessionHistory[chatID]
	filtered := make([]domain.SessionInfo, 0, len(history))
	for _, h := range history {
		if h.SessionID != sessionID {
			filtered = append(filtered, h)
		}
	}
	s.sessionHistory[chatID] = filtered
}

func (s *AcpAgentService) createNewSession(
	ctx context.Context,
	chatID string,
	live *liveSession,
	targetWorkspace string,
) error {
	sessCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()

	mcpServers := s.cfg.MCPServers
	if mcpServers == nil {
		mcpServers = []acpsdk.McpServer{}
	}
	newSess, err := live.conn.NewSession(sessCtx, acpsdk.NewSessionRequest{
		Cwd:        targetWorkspace,
		McpServers: mcpServers,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	live.sessionID = string(newSess.SessionId)
	live.workspace = targetWorkspace
	live.models = newSess.Models
	s.sessionHistory[chatID] = upsertCappedSessionHistory(s.sessionHistory[chatID], domain.SessionInfo{
		SessionID: live.sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()

	s.autoSwitchModel(ctx, live)

	return nil
}

const autoSwitchModelTimeout = 10 * time.Second

// modelInList returns true if id is in the available models list.
func modelInList(available []acpsdk.ModelInfo, id string) bool {
	for _, m := range available {
		if string(m.ModelId) == id {
			return true
		}
	}
	return false
}

// autoSwitchModel switches to the configured default model if the agent supports
// model switching and the default model is available.
func (s *AcpAgentService) autoSwitchModel(ctx context.Context, live *liveSession) {
	if s.cfg.DefaultModel == "" || live.models == nil {
		return
	}
	if string(live.models.CurrentModelId) == s.cfg.DefaultModel {
		return
	}
	if !modelInList(live.models.AvailableModels, s.cfg.DefaultModel) {
		slog.Debug("configured default model not found in available models", "model", s.cfg.DefaultModel)
		return
	}

	switchCtx, cancel := context.WithTimeout(ctx, autoSwitchModelTimeout)
	defer cancel()
	if _, err := live.conn.SetSessionModel(switchCtx, acpsdk.SetSessionModelRequest{
		SessionId: acpsdk.SessionId(live.sessionID),
		ModelId:   acpsdk.ModelId(s.cfg.DefaultModel),
	}); err != nil {
		slog.Warn("failed to auto-switch model",
			"model", s.cfg.DefaultModel,
			"session", live.sessionID,
			"error", err,
		)
		return
	}
	s.mu.Lock()
	live.models.CurrentModelId = acpsdk.ModelId(s.cfg.DefaultModel)
	s.mu.Unlock()
	slog.Info("auto-switched model", "model", s.cfg.DefaultModel, "session", live.sessionID)
}
