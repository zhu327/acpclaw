package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/domain"
)

// ServiceConfig configures the ACP agent service.
type ServiceConfig struct {
	AgentCommand   []string      // e.g. ["claude", "--no-color"]
	Workspace      string        // default workspace
	ConnectTimeout time.Duration // timeout for initialize + new_session
	ListTimeout    time.Duration // timeout for session/list calls; defaults to 5s
	PermissionMode domain.PermissionMode
	EventOutput    string // "stdout" or "off"
	MCPServers     []acpsdk.McpServer
	DefaultModel   string // optional model ID to auto-select on new sessions
	// AgentEnv is the explicit set of env var names to pass to agent subprocesses.
	// When nil, a safe default allowlist is used (PATH, HOME, LANG, etc.).
	// Set to an empty slice to pass no env vars at all.
	AgentEnv []string
}

var (
	_ domain.SessionManager        = (*AcpAgentService)(nil)
	_ domain.Prompter              = (*AcpAgentService)(nil)
	_ domain.PromptResponderSource = (*AcpAgentService)(nil)
	_ domain.PermissionHandler     = (*AcpAgentService)(nil)
	_ domain.ActivityObserver      = (*AcpAgentService)(nil)
	_ domain.ModelManager          = (*AcpAgentService)(nil)
	_ domain.ModeManager           = (*AcpAgentService)(nil)
)

// AcpAgentService manages ACP agent subprocesses per chat. Each chat maintains a long-lived
// ACP process; session operations run on the same connection.
type AcpAgentService struct {
	cfg            ServiceConfig
	ctx            context.Context
	cancel         context.CancelFunc
	liveByChat     map[string]*liveSession
	sessionHistory map[string][]domain.SessionInfo // Local session history when agent lacks session/list.
	mu             sync.RWMutex
	promptLocks    sync.Map // map[string]*sync.Mutex
	sessionLocks   sync.Map // map[string]*sync.Mutex
	onActivity     func(domain.ChatRef, domain.ActivityBlock)
	onPermission   func(domain.ChatRef, domain.PermissionRequest) <-chan domain.PermissionResponse
}

// NewAgentService creates a new ACP agent service. The returned service owns a
// background context that keeps agent subprocesses alive until Shutdown is called.
func NewAgentService(cfg ServiceConfig) *AcpAgentService {
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 30 * time.Second
	}
	if cfg.ListTimeout == 0 {
		cfg.ListTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &AcpAgentService{
		cfg:            cfg,
		ctx:            ctx,
		cancel:         cancel,
		liveByChat:     make(map[string]*liveSession),
		sessionHistory: make(map[string][]domain.SessionInfo),
	}
}

// SetActivityHandler sets the callback for activity updates.
func (s *AcpAgentService) SetActivityHandler(fn func(chat domain.ChatRef, block domain.ActivityBlock)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = fn
}

// SetPermissionHandler sets the callback for permission requests.
func (s *AcpAgentService) SetPermissionHandler(
	fn func(chat domain.ChatRef, req domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPermission = fn
}

// ActiveSession returns the active session info for the chat, or nil.
func (s *AcpAgentService) ActiveSession(chat domain.ChatRef) *domain.SessionInfo {
	key := chat.CompositeKey()
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.liveByChat[key]
	if live == nil {
		return nil
	}
	return &domain.SessionInfo{SessionID: live.sessionID, Workspace: live.workspace}
}

// promptLockFor returns the per-chat mutex that serializes Prompt calls.
//
// Two-lock design: promptLock and sessionLock are intentionally separate.
//   - promptLock (this one) serializes concurrent Prompt calls for the same chat.
//   - sessionLock (see sessionLockFor) guards session lifecycle: NewSession/LoadSession/Reconnect.
//
// This means Reconnect can concurrently interrupt an in-flight Prompt: killing the agent
// subprocess causes live.conn.Prompt to return an I/O error, which Prompt returns to
// the caller. This is the intended cancellation path — do not merge the two locks.
func (s *AcpAgentService) promptLockFor(key string) *sync.Mutex {
	v, _ := s.promptLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *AcpAgentService) sessionLockFor(key string) *sync.Mutex {
	v, _ := s.sessionLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// NewSession creates a new session; respawns the process when workspace changes.
func (s *AcpAgentService) NewSession(ctx context.Context, chat domain.ChatRef, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	key := chat.CompositeKey()
	sessionLock := s.sessionLockFor(key)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	live, err := s.ensureProcess(ctx, key, workspace)
	if err != nil {
		return err
	}
	targetWorkspace, err := s.resolveSessionWorkspace(live.workspace, workspace)
	if err != nil {
		return err
	}

	if targetWorkspace != live.workspace {
		slog.Info("workspace changed, respawning ACP process",
			"chat", key,
			"old_workspace", live.workspace,
			"new_workspace", targetWorkspace,
		)
		stopLiveSession(s.detachLiveSession(key))
		s.mu.Lock()
		delete(s.sessionHistory, key)
		s.mu.Unlock()
		live, err = s.ensureProcess(ctx, key, targetWorkspace)
		if err != nil {
			return err
		}
	}

	return s.createNewSession(ctx, key, live, targetWorkspace)
}

// LoadSession loads an existing session on the ACP process.
func (s *AcpAgentService) LoadSession(ctx context.Context, chat domain.ChatRef, sessionID, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	key := chat.CompositeKey()
	sessionLock := s.sessionLockFor(key)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	live, err := s.ensureProcess(ctx, key, workspace)
	if err != nil {
		return err
	}

	if !live.supportsLoadSession {
		return domain.ErrLoadSessionNotSupported
	}

	sessCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()

	targetWorkspace, err := s.resolveSessionWorkspace(live.workspace, workspace)
	if err != nil {
		return err
	}

	loadResp, err := live.conn.LoadSession(sessCtx, acpsdk.LoadSessionRequest{
		SessionId:  acpsdk.SessionId(sessionID),
		Cwd:        targetWorkspace,
		McpServers: s.cfg.MCPServers,
	})
	if err != nil {
		if strings.Contains(err.Error(), sessionNotFoundPhrase) {
			s.removeSessionFromHistory(key, sessionID)
			return fmt.Errorf("%w: %s", domain.ErrSessionNotFound, sessionID)
		}
		return err
	}

	s.mu.Lock()
	live.sessionID = sessionID
	live.workspace = targetWorkspace
	live.models = loadResp.Models
	live.modes = loadResp.Modes
	s.sessionHistory[key] = upsertCappedSessionHistory(s.sessionHistory[key], domain.SessionInfo{
		SessionID: sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()

	return nil
}

// ListSessions returns sessions from the agent or local history fallback.
func (s *AcpAgentService) ListSessions(ctx context.Context, chat domain.ChatRef) ([]domain.SessionInfo, error) {
	key := chat.CompositeKey()
	s.mu.RLock()
	live := s.liveByChat[key]
	s.mu.RUnlock()

	if live == nil {
		return nil, domain.ErrNoActiveProcess
	}

	if live.supportsSessionList {
		items, err := callSessionList(ctx, live.rawConn, "", s.cfg.ListTimeout)
		if err != nil {
			return nil, err
		}
		result := make([]domain.SessionInfo, 0, len(items))
		for _, item := range items {
			result = append(result, sessionItemToSessionInfo(item))
		}
		return result, nil
	}

	s.mu.RLock()
	history := s.sessionHistory[key]
	s.mu.RUnlock()
	if len(history) == 0 {
		return nil, nil
	}
	result := make([]domain.SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// Reconnect kills the ACP process and respawns with a new session.
func (s *AcpAgentService) Reconnect(ctx context.Context, chat domain.ChatRef, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	key := chat.CompositeKey()
	sessionLock := s.sessionLockFor(key)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	stopLiveSession(s.detachLiveSession(key))

	live, err := s.ensureProcess(ctx, key, workspace)
	if err != nil {
		return err
	}
	if err := s.createNewSession(ctx, key, live, live.workspace); err != nil {
		stopLiveSession(s.detachLiveSession(key))
		return err
	}

	s.mu.Lock()
	if len(s.sessionHistory[key]) > 1 {
		s.sessionHistory[key] = s.sessionHistory[key][len(s.sessionHistory[key])-1:]
	}
	s.mu.Unlock()
	return nil
}

// acpStdioLimitErrPhrase is the substring the ACP SDK embeds in its chunk-too-large error.
// This is fragile: if the SDK changes its message, detection silently breaks.
// Tracked: replace with errors.As once the SDK exposes a typed sentinel.
const acpStdioLimitErrPhrase = "chunk is longer than limit"

// sessionNotFoundPhrase is the substring used to detect "not found" errors from the agent.
// Fragile: relies on agent error message format. Replace with typed error once SDK exposes one.
const sessionNotFoundPhrase = "not found"

// ActivePromptResponder returns the Responder bound to the current in-flight Prompt, if any.
func (s *AcpAgentService) ActivePromptResponder(chat domain.ChatRef) domain.Responder {
	key := chat.CompositeKey()
	s.mu.RLock()
	live := s.liveByChat[key]
	s.mu.RUnlock()
	if live == nil {
		return nil
	}
	return live.activePromptResponder()
}

// Prompt sends a prompt to the agent and returns the reply.
func (s *AcpAgentService) Prompt(
	ctx context.Context,
	chat domain.ChatRef,
	input domain.PromptInput,
	resp domain.Responder,
) (*domain.AgentReply, error) {
	key := chat.CompositeKey()
	lock := s.promptLockFor(key)
	lock.Lock()
	defer lock.Unlock()

	s.mu.RLock()
	live := s.liveByChat[key]
	onActivity := s.onActivity
	onPermission := s.onPermission
	s.mu.RUnlock()

	if live == nil {
		return nil, domain.ErrNoActiveSession
	}

	live.setPromptResponder(resp)
	defer live.clearPromptResponder()

	slog.Info("Prompt to ACP",
		"chat", key,
		"session_id", live.sessionID,
		"text", logTextPreview(input.Text, 200),
	)

	s.setupPromptCallbacks(live, chat, onActivity, onPermission)

	live.client.StartCapture()
	blocks := BuildContentBlocks(input)
	_, err := live.conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(live.sessionID),
		Prompt:    blocks,
	})
	reply := live.client.FinishCapture()
	reply = s.ResolveFileURIResources(reply, live.workspace)
	if err != nil {
		if strings.Contains(err.Error(), acpStdioLimitErrPhrase) {
			return reply, fmt.Errorf("%w: %v", domain.ErrAgentOutputLimitExceeded, err)
		}
		return reply, err
	}
	return reply, nil
}

// setupPromptCallbacks wires activity and permission callbacks for prompt execution.
func (s *AcpAgentService) setupPromptCallbacks(
	live *liveSession,
	chat domain.ChatRef,
	onActivity func(domain.ChatRef, domain.ActivityBlock),
	onPermission func(domain.ChatRef, domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	logEvents := shouldLogEventOutput(s.cfg.EventOutput)
	permMode := live.permMode
	key := chat.CompositeKey()

	live.client.SetCallbacks(
		func(b domain.ActivityBlock) {
			if logEvents {
				slog.Info("ACP activity event",
					"chat", key, "session_id", live.sessionID,
					"kind", b.Kind, "status", b.Status,
					"detail", logTextPreview(b.Detail, 200),
					"text", logTextPreview(b.Text, 200),
				)
			}
			if onActivity != nil {
				onActivity(chat, b)
			}
		},
		func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
			if logEvents {
				slog.Info("ACP permission event",
					"chat", key, "session_id", live.sessionID,
					"request_id", req.ID, "tool", logTextPreview(req.Tool, 200),
				)
			}
			return s.permissionResponseChan(permMode, chat, req, onPermission)
		},
	)
}

func (s *AcpAgentService) permissionResponseChan(
	permMode domain.PermissionMode,
	chat domain.ChatRef,
	req domain.PermissionRequest,
	onPermission func(domain.ChatRef, domain.PermissionRequest) <-chan domain.PermissionResponse,
) <-chan domain.PermissionResponse {
	ch := make(chan domain.PermissionResponse, 1)
	switch permMode {
	case domain.PermissionModeApprove:
		ch <- domain.PermissionResponse{Decision: domain.PermissionAlways}
		return ch
	case domain.PermissionModeDeny:
		ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}
		return ch
	}
	if onPermission != nil {
		return onPermission(chat, req)
	}
	ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}
	return ch
}

// Cancel cancels the active prompt for the chat.
func (s *AcpAgentService) Cancel(ctx context.Context, chat domain.ChatRef) error {
	key := chat.CompositeKey()
	s.mu.RLock()
	live := s.liveByChat[key]
	s.mu.RUnlock()

	if live == nil {
		return domain.ErrNoActiveSession
	}
	return live.conn.Cancel(ctx, acpsdk.CancelNotification{
		SessionId: acpsdk.SessionId(live.sessionID),
	})
}

// Shutdown stops all agent sessions and cancels the service context.
func (s *AcpAgentService) Shutdown() {
	s.cancel()
	s.mu.Lock()
	sessions := make([]*liveSession, 0, len(s.liveByChat))
	for _, live := range s.liveByChat {
		sessions = append(sessions, live)
	}
	s.liveByChat = make(map[string]*liveSession)
	s.mu.Unlock()
	for _, live := range sessions {
		stopLiveSession(live)
	}
}

// GetModelState returns the current model state for the chat's live session.
func (s *AcpAgentService) GetModelState(chat domain.ChatRef) (*domain.ModelState, error) {
	key := chat.CompositeKey()
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.liveByChat[key]
	if live == nil {
		return nil, domain.ErrNoActiveSession
	}
	if live.models == nil {
		return nil, domain.ErrModelsNotSupported
	}
	models := live.models
	state := &domain.ModelState{CurrentModelID: string(models.CurrentModelId)}
	for _, m := range models.AvailableModels {
		info := domain.ModelInfo{
			ID:   string(m.ModelId),
			Name: m.Name,
		}
		if m.Description != nil {
			info.Description = *m.Description
		}
		state.Available = append(state.Available, info)
	}
	return state, nil
}

// SetSessionModel switches the model for the chat's active session.
func (s *AcpAgentService) SetSessionModel(ctx context.Context, chat domain.ChatRef, modelID string) error {
	key := chat.CompositeKey()
	s.mu.RLock()
	live := s.liveByChat[key]
	if live == nil {
		s.mu.RUnlock()
		return domain.ErrNoActiveSession
	}
	if live.models == nil {
		s.mu.RUnlock()
		return domain.ErrModelsNotSupported
	}
	if !modelInList(live.models.AvailableModels, modelID) {
		s.mu.RUnlock()
		return domain.ErrModelNotFound
	}
	sessionID := live.sessionID
	s.mu.RUnlock()

	_, err := live.conn.SetSessionModel(ctx, acpsdk.SetSessionModelRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ModelId:   acpsdk.ModelId(modelID),
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	live.models.CurrentModelId = acpsdk.ModelId(modelID)
	s.mu.Unlock()
	return nil
}

// SetSessionPermissionMode updates the permission mode for the chat's live session.
func (s *AcpAgentService) SetSessionPermissionMode(chat domain.ChatRef, mode domain.PermissionMode) {
	key := chat.CompositeKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.liveByChat[key]; live != nil {
		live.permMode = mode
	}
}

// GetModeState returns the current mode state for the chat's live session.
func (s *AcpAgentService) GetModeState(chat domain.ChatRef) (*domain.ModeState, error) {
	key := chat.CompositeKey()
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.liveByChat[key]
	if live == nil {
		return nil, domain.ErrNoActiveSession
	}
	if live.modes == nil {
		return nil, domain.ErrModesNotSupported
	}
	modes := live.modes
	state := &domain.ModeState{CurrentModeID: string(modes.CurrentModeId)}
	for _, m := range modes.AvailableModes {
		info := domain.ModeInfo{
			ID:   string(m.Id),
			Name: m.Name,
		}
		if m.Description != nil {
			info.Description = *m.Description
		}
		state.Available = append(state.Available, info)
	}
	return state, nil
}

// SetSessionMode switches the mode for the chat's active session.
func (s *AcpAgentService) SetSessionMode(ctx context.Context, chat domain.ChatRef, modeID string) error {
	key := chat.CompositeKey()
	s.mu.RLock()
	live := s.liveByChat[key]
	if live == nil {
		s.mu.RUnlock()
		return domain.ErrNoActiveSession
	}
	if live.modes == nil {
		s.mu.RUnlock()
		return domain.ErrModesNotSupported
	}
	if !modeInList(live.modes.AvailableModes, modeID) {
		s.mu.RUnlock()
		return domain.ErrModeNotFound
	}
	sessionID := live.sessionID
	s.mu.RUnlock()

	_, err := live.conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ModeId:    acpsdk.SessionModeId(modeID),
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	live.modes.CurrentModeId = acpsdk.SessionModeId(modeID)
	s.mu.Unlock()
	return nil
}
