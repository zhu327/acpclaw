package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/acpclient"
	"github.com/zhu327/acpclaw/internal/domain"
	"github.com/zhu327/acpclaw/internal/session"
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
	AgentEnv     []string
	SessionStore *session.Store
}

type liveSession struct {
	sessionID           string
	workspace           string
	cmd                 *exec.Cmd
	conn                *acpsdk.ClientSideConnection
	rawConn             *acpsdk.Connection // for session/list calls not yet in SDK
	client              *acpclient.AcpClient
	permMode            domain.PermissionMode
	supportsLoadSession bool
	supportsSessionList bool
}

var _ domain.AgentService = (*AcpAgentService)(nil)

// AcpAgentService manages ACP agent subprocesses per chat. Each chatID maintains a long-lived
// ACP process; session operations run on the same connection.
type AcpAgentService struct {
	cfg            ServiceConfig
	ctx            context.Context
	cancel         context.CancelFunc
	liveByChat     map[int64]*liveSession
	sessionHistory map[int64][]domain.SessionInfo // Local session history when agent lacks session/list.
	mu             sync.RWMutex
	promptLocks    sync.Map // map[int64]*sync.Mutex
	sessionLocks   sync.Map // map[int64]*sync.Mutex
	onActivity     func(int64, domain.ActivityBlock)
	onPermission   func(int64, domain.PermissionRequest) <-chan domain.PermissionResponse
	sessionStore   *session.Store
}

// defaultAgentEnvAllowlist is the set of env var names passed to agent subprocesses
// when AgentEnv is nil. It excludes secrets that may be present in the bot process
// environment (e.g. TELEGRAM_BOT_TOKEN, API keys).
var defaultAgentEnvAllowlist = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL",
	"LANG", "LC_ALL", "LC_CTYPE", "TERM",
	"TMPDIR", "TMP", "TEMP",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
	"SSH_AUTH_SOCK", "GPG_AGENT_INFO",
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
		liveByChat:     make(map[int64]*liveSession),
		sessionHistory: make(map[int64][]domain.SessionInfo),
		sessionStore:   cfg.SessionStore,
	}
}

// SetActivityHandler sets the callback for activity updates.
func (s *AcpAgentService) SetActivityHandler(fn func(chatID int64, block domain.ActivityBlock)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = fn
}

// SetPermissionHandler sets the callback for permission requests.
func (s *AcpAgentService) SetPermissionHandler(fn func(chatID int64, req domain.PermissionRequest) <-chan domain.PermissionResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPermission = fn
}

// ActiveSession returns the active session info for the chat, or nil.
func (s *AcpAgentService) ActiveSession(chatID int64) *domain.SessionInfo {
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
func (s *AcpAgentService) promptLockFor(chatID int64) *sync.Mutex {
	v, _ := s.promptLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *AcpAgentService) sessionLockFor(chatID int64) *sync.Mutex {
	v, _ := s.sessionLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *AcpAgentService) detachLiveSession(chatID int64) *liveSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveByChat[chatID]
	if live == nil {
		return nil
	}
	delete(s.liveByChat, chatID)
	return live
}

func stopLiveSession(live *liveSession) {
	if live == nil {
		return
	}
	if live.client != nil {
		live.client.ReleaseSessionTerminals(live.sessionID)
	}
	if live.cmd == nil || live.cmd.Process == nil {
		return
	}
	_ = live.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- live.cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		_ = live.cmd.Process.Kill()
		<-done
	}
}

func isProcessAlive(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func (s *AcpAgentService) resolveWorkspace(workspace string) string {
	if workspace != "" {
		return workspace
	}
	if s.cfg.Workspace != "" {
		return s.cfg.Workspace
	}
	wd, err := os.Getwd()
	if err != nil {
		slog.Warn("failed to get working directory, using '.'", "error", err)
		return "."
	}
	return wd
}

func (s *AcpAgentService) ensureAbsPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	wd, err := os.Getwd()
	if err != nil {
		slog.Warn("failed to get working directory for absolute path resolution", "error", err)
		return p
	}
	return filepath.Join(wd, p)
}

func (s *AcpAgentService) prepareWorkspace(workspace string) (string, error) {
	workspace = s.ensureAbsPath(s.resolveWorkspace(workspace))
	info, statErr := os.Stat(workspace)
	if statErr == nil && !info.IsDir() {
		return "", fmt.Errorf("workspace path exists but is not a directory: %s", workspace)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", fmt.Errorf("failed to create workspace directory: %w", err)
	}
	return workspace, nil
}

func (s *AcpAgentService) resolveSessionWorkspace(currentWorkspace, requestedWorkspace string) (string, error) {
	if strings.TrimSpace(requestedWorkspace) == "" {
		return currentWorkspace, nil
	}
	return s.prepareWorkspace(requestedWorkspace)
}

func shouldLogEventOutput(eventOutput string) bool {
	s := strings.ToLower(strings.TrimSpace(eventOutput))
	return s == "" || s == "stdout"
}

type spawnResult struct {
	cmd                 *exec.Cmd
	conn                *acpsdk.ClientSideConnection
	rawConn             *acpsdk.Connection // extracted via unsafe for session/list calls not yet in SDK
	client              *acpclient.AcpClient
	initResp            *acpsdk.InitializeResponse
	supportsSessionList bool   // determined from agentCapabilities.sessionCapabilities.list
	workspace           string // prepared workspace path for Cwd
}

// extendedInitResponse adds sessionCapabilities (in ACP spec, not yet in Go SDK v0.6.3).
type extendedInitResponse struct {
	Meta              any                       `json:"_meta,omitempty"`
	AgentCapabilities extendedAgentCapabilities `json:"agentCapabilities,omitempty"`
	AgentInfo         *acpsdk.Implementation    `json:"agentInfo,omitempty"`
	AuthMethods       []acpsdk.AuthMethod       `json:"authMethods"`
	ProtocolVersion   acpsdk.ProtocolVersion    `json:"protocolVersion"`
}

type extendedAgentCapabilities struct {
	Meta                any                       `json:"_meta,omitempty"`
	LoadSession         bool                      `json:"loadSession,omitempty"`
	McpCapabilities     acpsdk.McpCapabilities    `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  acpsdk.PromptCapabilities `json:"promptCapabilities,omitempty"`
	SessionCapabilities *sessionCapabilities      `json:"sessionCapabilities,omitempty"`
}

type sessionCapabilities struct {
	List *struct{} `json:"list,omitempty"`
}

func (r *extendedInitResponse) toSDKResponse() acpsdk.InitializeResponse {
	return acpsdk.InitializeResponse{
		Meta: r.Meta,
		AgentCapabilities: acpsdk.AgentCapabilities{
			Meta:               r.AgentCapabilities.Meta,
			LoadSession:        r.AgentCapabilities.LoadSession,
			McpCapabilities:    r.AgentCapabilities.McpCapabilities,
			PromptCapabilities: r.AgentCapabilities.PromptCapabilities,
		},
		AgentInfo:       r.AgentInfo,
		AuthMethods:     r.AuthMethods,
		ProtocolVersion: r.ProtocolVersion,
	}
}

func (r *extendedInitResponse) supportsSessionListCap() bool {
	return r.AgentCapabilities.SessionCapabilities != nil &&
		r.AgentCapabilities.SessionCapabilities.List != nil
}

// extractRawConn extracts the private *acpsdk.Connection from a ClientSideConnection.
//
// FRAGILE: This relies on the undocumented memory layout of acpsdk.ClientSideConnection
// (SDK v0.6.3): field 0 is *Connection, field 1 is Client. If the SDK adds or reorders
// fields, this silently returns a garbage pointer and causes a crash or memory corruption.
//
// This is necessary because acpsdk.SendRequest requires *Connection directly, and
// session/list is not yet exposed as a typed method in the Go SDK (v0.6.3).
//
// Mitigation: TestExtractRawConn in service_internal_test.go verifies the cast at test time.
// Pin the SDK version tightly in go.mod and re-run tests after any SDK upgrade.
// Track: replace with a typed SDK method once session/list is exposed upstream.
func extractRawConn(csc *acpsdk.ClientSideConnection) *acpsdk.Connection {
	// *ClientSideConnection → first pointer-sized field is *Connection.
	return *(**acpsdk.Connection)(unsafe.Pointer(csc))
}

func (s *AcpAgentService) buildAgentEnv() []string {
	allowlist := s.cfg.AgentEnv
	if allowlist == nil {
		allowlist = defaultAgentEnvAllowlist
	}
	parentEnv := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			parentEnv[kv[:idx]] = kv[idx+1:]
		}
	}
	env := make([]string, 0, len(allowlist))
	for _, name := range allowlist {
		if val, ok := parentEnv[name]; ok {
			env = append(env, name+"="+val)
		}
	}
	return env
}

func (s *AcpAgentService) spawnAndInitialize(ctx context.Context, workspace string) (*spawnResult, error) {
	cmd := exec.CommandContext(s.ctx, s.cfg.AgentCommand[0], s.cfg.AgentCommand[1:]...)
	cmd.Env = s.buildAgentEnv()
	if workspace != "" {
		cmd.Dir = workspace
	}
	cmd.Stderr = &slogWriter{level: slog.LevelWarn, msg: "agent stderr"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	client := acpclient.NewAcpClient(nil, nil)
	conn := acpsdk.NewClientSideConnection(client, stdin, stdout)

	initCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()

	rawConn := extractRawConn(conn)
	extResp, err := acpsdk.SendRequest[extendedInitResponse](
		rawConn,
		initCtx,
		acpsdk.AgentMethodInitialize,
		acpsdk.InitializeRequest{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			ClientCapabilities: acpsdk.ClientCapabilities{
				Terminal: true,
			},
		},
	)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	sdkResp := extResp.toSDKResponse()
	return &spawnResult{
		cmd:                 cmd,
		conn:                conn,
		rawConn:             rawConn,
		client:              client,
		initResp:            &sdkResp,
		supportsSessionList: extResp.supportsSessionListCap(),
		workspace:           workspace,
	}, nil
}

type listSessionsRequest struct {
	Cursor *string `json:"cursor,omitempty"`
	Cwd    *string `json:"cwd,omitempty"`
}

type listSessionsResponse struct {
	Sessions   []sessionListItem `json:"sessions"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

type sessionListItem struct {
	SessionID string  `json:"sessionId"`
	Cwd       string  `json:"cwd"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

func sessionItemToSessionInfo(item sessionListItem) domain.SessionInfo {
	info := domain.SessionInfo{SessionID: item.SessionID, Workspace: item.Cwd}
	if item.Title != nil {
		info.Title = *item.Title
	}
	if item.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *item.UpdatedAt); err == nil {
			info.UpdatedAt = t
		}
	}
	return info
}

func callSessionList(
	ctx context.Context,
	conn *acpsdk.Connection,
	cwd string,
	timeout time.Duration,
) ([]sessionListItem, error) {
	var sessions []sessionListItem
	var cursor *string
	for range 5 { // max 5 pages
		req := listSessionsRequest{Cursor: cursor}
		if cwd != "" {
			req.Cwd = &cwd
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := acpsdk.SendRequest[listSessionsResponse](conn, callCtx, "session/list", req)
		cancel()
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, resp.Sessions...)
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return sessions, nil
}

const maxSessionHistory = 20

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

func (s *AcpAgentService) attachSession(chatID int64, live *liveSession) {
	s.mu.Lock()
	s.liveByChat[chatID] = live
	s.mu.Unlock()
}

func (s *AcpAgentService) removeSessionFromHistory(chatID int64, sessionID string) {
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
	chatID int64,
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
	s.sessionHistory[chatID] = upsertCappedSessionHistory(s.sessionHistory[chatID], domain.SessionInfo{
		SessionID: live.sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()

	s.writeSessionContext(chatID)
	return nil
}

// writeSessionContext writes chat context for MCP tools; failures do not affect the main flow.
func (s *AcpAgentService) writeSessionContext(chatID int64) {
	if s.sessionStore == nil {
		return
	}
	channel := s.cfg.ChannelName
	if channel == "" {
		channel = "telegram" // backward compatibility default
	}
	if err := s.sessionStore.Write(channel, strconv.FormatInt(chatID, 10)); err != nil {
		slog.Warn("failed to write session context", "chatID", chatID, "channel", channel, "error", err)
	}
}

// ensureProcess ensures an active ACP process for chatID. Caller must hold sessionLock.
func (s *AcpAgentService) ensureProcess(ctx context.Context, chatID int64, workspace string) (*liveSession, error) {
	s.mu.RLock()
	live := s.liveByChat[chatID]
	s.mu.RUnlock()
	if live != nil && live.cmd != nil && isProcessAlive(live.cmd.Process) {
		return live, nil
	}
	if live != nil {
		slog.Warn("stale ACP process detected, recreating",
			"chat_id", chatID,
			"session_id", live.sessionID,
		)
		stopLiveSession(s.detachLiveSession(chatID))
	}

	ws, err := s.prepareWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	sr, err := s.spawnAndInitialize(ctx, ws)
	if err != nil {
		return nil, err
	}
	live = &liveSession{
		workspace:           ws,
		cmd:                 sr.cmd,
		conn:                sr.conn,
		rawConn:             sr.rawConn,
		client:              sr.client,
		permMode:            s.cfg.PermissionMode,
		supportsLoadSession: sr.initResp.AgentCapabilities.LoadSession,
		supportsSessionList: sr.supportsSessionList,
	}
	s.attachSession(chatID, live)
	return live, nil
}

// NewSession creates a new session; respawns the process when workspace changes.
func (s *AcpAgentService) NewSession(ctx context.Context, chatID int64, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return ErrAgentCommandNotConfigured
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
func (s *AcpAgentService) LoadSession(ctx context.Context, chatID int64, sessionID, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return ErrAgentCommandNotConfigured
	}
	sessionLock := s.sessionLockFor(chatID)
	sessionLock.Lock()
	defer sessionLock.Unlock()

	live, err := s.ensureProcess(ctx, chatID, workspace)
	if err != nil {
		return err
	}

	if !live.supportsLoadSession {
		return ErrLoadSessionNotSupported
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
			return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
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

	s.writeSessionContext(chatID)
	return nil
}

// ListSessions returns sessions from the agent or local history fallback.
func (s *AcpAgentService) ListSessions(ctx context.Context, chatID int64) ([]domain.SessionInfo, error) {
	s.mu.RLock()
	live := s.liveByChat[chatID]
	s.mu.RUnlock()

	if live == nil {
		return nil, ErrNoActiveProcess
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
func (s *AcpAgentService) Reconnect(ctx context.Context, chatID int64, workspace string) error {
	if len(s.cfg.AgentCommand) == 0 {
		return ErrAgentCommandNotConfigured
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

// ErrAgentOutputLimitExceeded indicates the agent's output exceeded the stdio limit.
var ErrAgentOutputLimitExceeded = errors.New("agent output exceeded ACP stdio limit")

// acpStdioLimitErrPhrase is the substring the ACP SDK embeds in its chunk-too-large error.
// This is fragile: if the SDK changes its message, detection silently breaks.
// Tracked: replace with errors.As once the SDK exposes a typed sentinel.
const acpStdioLimitErrPhrase = "chunk is longer than limit"

// ErrLoadSessionNotSupported is returned when the agent does not support load_session.
var ErrLoadSessionNotSupported = errors.New("agent does not support load_session")

// ErrSessionNotFound is returned when the agent cannot find the requested session.
var ErrSessionNotFound = errors.New("session not found")

// sessionNotFoundPhrase is the substring used to detect "not found" errors from the agent.
// Fragile: relies on agent error message format. Replace with typed error once SDK exposes one.
const sessionNotFoundPhrase = "not found"

// ErrNoActiveProcess is returned when there is no active ACP process for the chat.
var ErrNoActiveProcess = errors.New("no active ACP process")

// fileURIResult is the typed result of resolveFileURI.
type fileURIResult struct {
	data        []byte
	name        string
	warning     string
	passThrough bool // true when the file should be forwarded as-is (non-local URI)
}

func extractFileURI(f domain.FileData) string {
	if strings.HasPrefix(f.Name, "file://") {
		return f.Name
	}
	if strings.HasPrefix(string(f.Data), "file://") {
		return strings.TrimSpace(string(f.Data))
	}
	return ""
}

func fileURIWarning(format string, args ...any) fileURIResult {
	return fileURIResult{warning: "Attachment warning: " + fmt.Sprintf(format, args...) + "\n"}
}

func resolveFileURI(f domain.FileData, workspaceAbs string) fileURIResult {
	fileURI := extractFileURI(f)
	if fileURI == "" {
		return fileURIResult{}
	}
	u, err := url.Parse(fileURI)
	if err != nil {
		return fileURIWarning("%s: invalid URI", fileURI)
	}
	if u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return fileURIResult{passThrough: true}
	}
	path, err := url.PathUnescape(u.Path)
	if err != nil {
		return fileURIWarning("%s: invalid path encoding", fileURI)
	}
	if path == "" {
		return fileURIWarning("%s: empty path after decode", fileURI)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	rel, err := filepath.Rel(workspaceAbs, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fileURIWarning("%s: path outside workspace", filepath.Base(path))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	if info.IsDir() {
		return fileURIWarning("%s: path is a directory, not a file", filepath.Base(path))
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	return fileURIResult{data: data, name: filepath.Base(resolved)}
}

// ResolveFileURIResources resolves file:// URIs in reply files to actual content.
func (s *AcpAgentService) ResolveFileURIResources(reply *domain.AgentReply, workspace string) *domain.AgentReply {
	if reply == nil {
		return nil
	}
	out := &domain.AgentReply{
		Text:       reply.Text,
		Images:     append([]domain.ImageData(nil), reply.Images...),
		Files:      nil,
		Activities: append([]domain.ActivityBlock(nil), reply.Activities...),
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		workspaceAbs = filepath.Clean(workspace)
	}
	if evaluatedAbs, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = evaluatedAbs
	}
	for _, f := range reply.Files {
		r := resolveFileURI(f, workspaceAbs)
		switch {
		case r.passThrough:
			out.Files = append(out.Files, f)
		case r.warning != "":
			out.Text += r.warning
		case r.data == nil:
			out.Files = append(out.Files, f)
		case strings.HasPrefix(f.MIMEType, "image/"):
			out.Images = append(out.Images, domain.ImageData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		default:
			out.Files = append(out.Files, domain.FileData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		}
	}
	return out
}

// Prompt sends a prompt to the agent and returns the reply.
func (s *AcpAgentService) Prompt(ctx context.Context, chatID int64, input domain.PromptInput) (*domain.AgentReply, error) {
	lock := s.promptLockFor(chatID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.RLock()
	live := s.liveByChat[chatID]
	onActivity := s.onActivity
	onPermission := s.onPermission
	s.mu.RUnlock()

	if live == nil {
		return nil, ErrNoActiveSession
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
			return reply, fmt.Errorf("%w: %v", ErrAgentOutputLimitExceeded, err)
		}
		return reply, err
	}
	return reply, nil
}

// setupPromptCallbacks wires activity and permission callbacks for prompt execution.
func (s *AcpAgentService) setupPromptCallbacks(
	live *liveSession,
	chatID int64,
	onActivity func(int64, domain.ActivityBlock),
	onPermission func(int64, domain.PermissionRequest) <-chan domain.PermissionResponse,
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
	chatID int64,
	req domain.PermissionRequest,
	onPermission func(int64, domain.PermissionRequest) <-chan domain.PermissionResponse,
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

// ErrNoActiveSession is returned when there is no active session for the chat.
var ErrNoActiveSession = errors.New("no active session")

// ErrAgentCommandNotConfigured is returned when no agent command is configured.
var ErrAgentCommandNotConfigured = errors.New("agent command not configured")

// Cancel cancels the active prompt for the chat.
func (s *AcpAgentService) Cancel(ctx context.Context, chatID int64) error {
	s.mu.RLock()
	live := s.liveByChat[chatID]
	s.mu.RUnlock()

	if live == nil {
		return ErrNoActiveSession
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
	s.liveByChat = make(map[int64]*liveSession)
	s.mu.Unlock()
	for _, live := range sessions {
		stopLiveSession(live)
	}
}

// SetSessionPermissionMode updates the permission mode for the chat's live session.
func (s *AcpAgentService) SetSessionPermissionMode(chatID int64, mode domain.PermissionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.liveByChat[chatID]; live != nil {
		live.permMode = mode
	}
}

type slogWriter struct {
	level slog.Level
	msg   string
}

func (w *slogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		slog.Log(context.Background(), w.level, w.msg, "output", line)
	}
	return len(p), nil
}

// BuildContentBlocks converts domain.PromptInput to SDK ContentBlock slice.
func BuildContentBlocks(input domain.PromptInput) []acpsdk.ContentBlock {
	var blocks []acpsdk.ContentBlock
	if input.Text != "" {
		blocks = append(blocks, acpsdk.TextBlock(input.Text))
	}
	for _, img := range input.Images {
		data := base64.StdEncoding.EncodeToString(img.Data)
		blocks = append(blocks, acpsdk.ImageBlock(data, img.MIMEType))
	}
	for _, f := range input.Files {
		name := f.Name
		if name == "" {
			name = "attachment.bin"
		}
		if f.TextContent != nil {
			// Text file semantic (Python parity): File: <name>\n\n<content>
			payload := "File: " + name + "\n\n" + *f.TextContent
			blocks = append(blocks, acpsdk.TextBlock(payload))
			continue
		}
		// Binary file semantic (Python parity): Binary file attached: <name> (<mime>)
		mime := f.MIMEType
		if mime == "" {
			mime = "unknown"
		}
		payload := "Binary file attached: " + name + " (" + mime + ")"
		blocks = append(blocks, acpsdk.TextBlock(payload))
	}
	return blocks
}

// logTextPreview returns a collapsed, truncated preview of text for log output
func logTextPreview(text string, maxLen int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return "<empty>"
	}
	if len(collapsed) <= maxLen {
		return collapsed
	}
	return collapsed[:maxLen] + "..."
}
