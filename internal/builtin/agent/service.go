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
	ChannelName    string // channel identifier (e.g., "telegram", "slack") for context tracking
	// AgentEnv is the explicit set of env var names to pass to agent subprocesses.
	// When nil, a safe default allowlist is used (PATH, HOME, LANG, etc.).
	// Set to an empty slice to pass no env vars at all.
	AgentEnv []string
}

var _ domain.AgentService = (*AcpAgentService)(nil)

// AcpAgentService manages ACP agent subprocesses per chat. Each chatID maintains a long-lived
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
	onActivity     func(string, domain.ActivityBlock)
	onPermission   func(string, domain.PermissionRequest) <-chan domain.PermissionResponse
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
func (s *AcpAgentService) SetActivityHandler(fn func(chatID string, block domain.ActivityBlock)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = fn
}

// SetPermissionHandler sets the callback for permission requests.
func (s *AcpAgentService) SetPermissionHandler(
	fn func(chatID string, req domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPermission = fn
}

// ActiveSession returns the active session info for the chat, or nil.
func (s *AcpAgentService) ActiveSession(chatID string) *domain.SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.liveByChat[chatID]
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
func (s *AcpAgentService) promptLockFor(chatID string) *sync.Mutex {
	v, _ := s.promptLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *AcpAgentService) sessionLockFor(chatID string) *sync.Mutex {
	v, _ := s.sessionLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// NewSession creates a new session; respawns the process when workspace changes.
func (s *AcpAgentService) NewSession(ctx context.Context, chatID string, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	sessionLock := s.sessionLockFor(chatID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	live, err := s.ensureProcess(ctx, chatID, workspace)
	if err != nil {
		return err
	}
	targetWorkspace, err := s.resolveSessionWorkspace(live.workspace, workspace)
	if err != nil {
		return err
	}

	if targetWorkspace != live.workspace {
		slog.Info("workspace changed, respawning ACP process",
			"chat_id", chatID,
			"old_workspace", live.workspace,
			"new_workspace", targetWorkspace,
		)
		stopLiveSession(s.detachLiveSession(chatID))
		s.mu.Lock()
		delete(s.sessionHistory, chatID)
		s.mu.Unlock()
		live, err = s.ensureProcess(ctx, chatID, targetWorkspace)
		if err != nil {
			return err
		}
	}

	return s.createNewSession(ctx, chatID, live, targetWorkspace)
}

// LoadSession loads an existing session on the ACP process.
func (s *AcpAgentService) LoadSession(ctx context.Context, chatID string, sessionID, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	sessionLock := s.sessionLockFor(chatID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	live, err := s.ensureProcess(ctx, chatID, workspace)
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

	_, err = live.conn.LoadSession(sessCtx, acpsdk.LoadSessionRequest{
		SessionId:  acpsdk.SessionId(sessionID),
		Cwd:        targetWorkspace,
		McpServers: s.cfg.MCPServers,
	})
	if err != nil {
		if strings.Contains(err.Error(), sessionNotFoundPhrase) {
			s.removeSessionFromHistory(chatID, sessionID)
			return fmt.Errorf("%w: %s", domain.ErrSessionNotFound, sessionID)
		}
		return err
	}

	s.mu.Lock()
	live.sessionID = sessionID
	live.workspace = targetWorkspace
	s.sessionHistory[chatID] = upsertCappedSessionHistory(s.sessionHistory[chatID], domain.SessionInfo{
		SessionID: sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()

	return nil
}

// ListSessions returns sessions from the agent or local history fallback.
func (s *AcpAgentService) ListSessions(ctx context.Context, chatID string) ([]domain.SessionInfo, error) {
	s.mu.RLock()
	live := s.liveByChat[chatID]
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
	history := s.sessionHistory[chatID]
	s.mu.RUnlock()
	if len(history) == 0 {
		return nil, nil
	}
	result := make([]domain.SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// Reconnect kills the ACP process and respawns with a new session.
func (s *AcpAgentService) Reconnect(ctx context.Context, chatID string, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return domain.ErrAgentCommandNotConfigured
	}
	sessionLock := s.sessionLockFor(chatID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	stopLiveSession(s.detachLiveSession(chatID))

	live, err := s.ensureProcess(ctx, chatID, workspace)
	if err != nil {
		return err
	}
	if err := s.createNewSession(ctx, chatID, live, live.workspace); err != nil {
		stopLiveSession(s.detachLiveSession(chatID))
		return err
	}

	s.mu.Lock()
	if len(s.sessionHistory[chatID]) > 1 {
		s.sessionHistory[chatID] = s.sessionHistory[chatID][len(s.sessionHistory[chatID])-1:]
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

// Prompt sends a prompt to the agent and returns the reply.
func (s *AcpAgentService) Prompt(
	ctx context.Context,
	chatID string,
	input domain.PromptInput,
) (*domain.AgentReply, error) {
	lock := s.promptLockFor(chatID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.RLock()
	live := s.liveByChat[chatID]
	onActivity := s.onActivity
	onPermission := s.onPermission
	s.mu.RUnlock()

	if live == nil {
		return nil, domain.ErrNoActiveSession
	}

	slog.Info("Prompt to ACP",
		"chat_id", chatID,
		"session_id", live.sessionID,
		"text", logTextPreview(input.Text, 200),
	)

	s.setupPromptCallbacks(live, chatID, onActivity, onPermission)

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
	chatID string,
	onActivity func(string, domain.ActivityBlock),
	onPermission func(string, domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	logEvents := shouldLogEventOutput(s.cfg.EventOutput)
	permMode := live.permMode

	live.client.SetCallbacks(
		func(b domain.ActivityBlock) {
			if logEvents {
				slog.Info("ACP activity event",
					"chat_id", chatID, "session_id", live.sessionID,
					"kind", b.Kind, "status", b.Status,
					"detail", logTextPreview(b.Detail, 200),
					"text", logTextPreview(b.Text, 200),
				)
			}
			if onActivity != nil {
				onActivity(chatID, b)
			}
		},
		func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
			if logEvents {
				slog.Info("ACP permission event",
					"chat_id", chatID, "session_id", live.sessionID,
					"request_id", req.ID, "tool", logTextPreview(req.Tool, 200),
				)
			}
			return s.permissionResponseChan(permMode, chatID, req, onPermission)
		},
	)
}

func (s *AcpAgentService) permissionResponseChan(
	permMode domain.PermissionMode,
	chatID string,
	req domain.PermissionRequest,
	onPermission func(string, domain.PermissionRequest) <-chan domain.PermissionResponse,
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
		return onPermission(chatID, req)
	}
	ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}
	return ch
}

// Cancel cancels the active prompt for the chat.
func (s *AcpAgentService) Cancel(ctx context.Context, chatID string) error {
	s.mu.RLock()
	live := s.liveByChat[chatID]
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

// SetSessionPermissionMode updates the permission mode for the chat's live session.
func (s *AcpAgentService) SetSessionPermissionMode(chatID string, mode domain.PermissionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.liveByChat[chatID]; live != nil {
		live.permMode = mode
	}
}
