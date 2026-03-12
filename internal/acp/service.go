package acp

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
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/util"
)

// ServiceConfig holds configuration for the ACP agent service.
type ServiceConfig struct {
	AgentCommand   []string      // e.g. ["claude", "--no-color"]
	Workspace      string        // default workspace
	ConnectTimeout time.Duration // timeout for initialize + new_session
	ListTimeout    time.Duration // timeout for session/list calls; defaults to 5s
	PermissionMode PermissionMode
	EventOutput    string // "stdout" or "off"
	MCPServers     []acpsdk.McpServer
	// AgentEnv is the explicit set of env var names to pass to agent subprocesses.
	// When nil, a safe default allowlist is used (PATH, HOME, LANG, etc.).
	// Set to an empty slice to pass no env vars at all.
	AgentEnv []string
}

// liveSession holds a running agent process and its ACP connection.
type liveSession struct {
	sessionID           string
	workspace           string
	cmd                 *exec.Cmd
	conn                *acpsdk.ClientSideConnection
	rawConn             *acpsdk.Connection // for session/list calls not yet in SDK
	client              *AcpClient
	permMode            PermissionMode
	supportsLoadSession bool
	supportsSessionList bool
}

// AcpAgentService manages ACP agent subprocesses per chat.
// 每个 chatID 维护一个长期存活的 ACP 进程，session 操作在同一连接上完成。
type AcpAgentService struct {
	cfg            ServiceConfig
	ctx            context.Context
	cancel         context.CancelFunc
	liveByChat     map[int64]*liveSession
	sessionHistory map[int64][]SessionInfo // 本地 session 历史，agent 不支持 session/list 时作为 fallback
	mu             sync.RWMutex
	promptLocks    sync.Map // map[int64]*sync.Mutex
	sessionLocks   sync.Map // map[int64]*sync.Mutex
	onActivity     func(int64, ActivityBlock)
	onPermission   func(int64, PermissionRequest) <-chan PermissionResponse
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
		sessionHistory: make(map[int64][]SessionInfo),
	}
}

// SetActivityHandler sets the callback for activity updates.
func (s *AcpAgentService) SetActivityHandler(fn func(chatID int64, block ActivityBlock)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = fn
}

// SetPermissionHandler sets the callback for permission requests.
func (s *AcpAgentService) SetPermissionHandler(fn func(chatID int64, req PermissionRequest) <-chan PermissionResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPermission = fn
}

// ActiveSession returns the active session info for the chat, or nil.
func (s *AcpAgentService) ActiveSession(chatID int64) *SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.liveByChat[chatID]
	if live == nil {
		return nil
	}
	return &SessionInfo{SessionID: live.sessionID, Workspace: live.workspace}
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

// sessionLockFor returns the per-chat mutex that guards session lifecycle
// (NewSession, LoadSession, Reconnect). See promptLockFor for the separate prompt lock.
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
	// Release all terminal subprocesses spawned during this session.
	if live.client != nil {
		live.client.terminals.ReleaseSession(live.sessionID)
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
	switch strings.ToLower(strings.TrimSpace(eventOutput)) {
	case "", "stdout":
		return true
	default:
		return false
	}
}

// spawnResult holds the output of spawnAndInitialize for creating a liveSession.
type spawnResult struct {
	cmd                 *exec.Cmd
	conn                *acpsdk.ClientSideConnection
	rawConn             *acpsdk.Connection // extracted via unsafe for session/list calls not yet in SDK
	client              *AcpClient
	initResp            *acpsdk.InitializeResponse
	supportsSessionList bool   // determined from agentCapabilities.sessionCapabilities.list
	workspace           string // prepared workspace path for Cwd
}

// extendedInitResponse mirrors acpsdk.InitializeResponse but adds sessionCapabilities
// which is present in the ACP protocol spec but not yet in the Go SDK (v0.6.3).
type extendedInitResponse struct {
	Meta              any                       `json:"_meta,omitempty"`
	AgentCapabilities extendedAgentCapabilities `json:"agentCapabilities,omitempty"`
	AgentInfo         *acpsdk.Implementation    `json:"agentInfo,omitempty"`
	AuthMethods       []acpsdk.AuthMethod       `json:"authMethods"`
	ProtocolVersion   acpsdk.ProtocolVersion    `json:"protocolVersion"`
}

// extendedAgentCapabilities extends acpsdk.AgentCapabilities with sessionCapabilities.
type extendedAgentCapabilities struct {
	Meta                any                       `json:"_meta,omitempty"`
	LoadSession         bool                      `json:"loadSession,omitempty"`
	McpCapabilities     acpsdk.McpCapabilities    `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  acpsdk.PromptCapabilities `json:"promptCapabilities,omitempty"`
	SessionCapabilities *sessionCapabilities      `json:"sessionCapabilities,omitempty"`
}

// sessionCapabilities describes session-related capabilities advertised by the agent.
type sessionCapabilities struct {
	List *struct{} `json:"list,omitempty"`
}

// toSDKResponse converts an extendedInitResponse back to acpsdk.InitializeResponse,
// dropping the extra sessionCapabilities field.
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

// supportsSessionListCap checks whether the agent advertised session/list support.
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

// buildAgentEnv constructs the environment for agent subprocesses.
// It uses AgentEnv as an allowlist of names to pass from the current process environment.
// When AgentEnv is nil, defaultAgentEnvAllowlist is used to avoid leaking secrets.
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

// spawnAndInitialize starts an agent subprocess, sets up ACP connection, and
// runs the Initialize handshake. The caller must kill cmd on subsequent errors.
// workspace sets the subprocess working directory so the agent starts in the
// correct project root; the same path is also passed via NewSessionRequest.Cwd.
func (s *AcpAgentService) spawnAndInitialize(ctx context.Context, workspace string) (*spawnResult, error) {
	cmd := exec.CommandContext(s.ctx, s.cfg.AgentCommand[0], s.cfg.AgentCommand[1:]...)
	// Use an explicit env allowlist to avoid leaking secrets (e.g. TELEGRAM_BOT_TOKEN)
	// into /proc/[pid]/environ of agent subprocesses. Configure AgentEnv in ServiceConfig
	// to pass additional variables required by the agent.
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

	client := NewAcpClient(nil, nil)
	conn := acpsdk.NewClientSideConnection(client, stdin, stdout)

	initCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()

	rawConn := extractRawConn(conn)
	extResp, err := acpsdk.SendRequest[extendedInitResponse](rawConn, initCtx, acpsdk.AgentMethodInitialize, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Terminal: true,
		},
	})
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

// listSessionsRequest is the request body for session/list (ACP protocol, not yet in Go SDK v0.6.3).
type listSessionsRequest struct {
	Cursor *string `json:"cursor,omitempty"`
	Cwd    *string `json:"cwd,omitempty"`
}

// listSessionsResponse is the response body for session/list.
type listSessionsResponse struct {
	Sessions   []sessionListItem `json:"sessions"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

// sessionListItem is a single entry in the session/list response.
type sessionListItem struct {
	SessionID string  `json:"sessionId"`
	Cwd       string  `json:"cwd"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

func sessionItemToSessionInfo(item sessionListItem) SessionInfo {
	info := SessionInfo{SessionID: item.SessionID, Workspace: item.Cwd}
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

// callSessionList calls session/list on the given connection with optional cwd filter.
func callSessionList(ctx context.Context, conn *acpsdk.Connection, cwd string, timeout time.Duration) ([]sessionListItem, error) {
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

// appendCappedSessionHistory appends a session to the history list, capping at maxSessionHistory.
func appendCappedSessionHistory(history []SessionInfo, info SessionInfo) []SessionInfo {
	history = append(history, info)
	if len(history) > maxSessionHistory {
		history = history[len(history)-maxSessionHistory:]
	}
	return history
}

func (s *AcpAgentService) attachSession(chatID int64, live *liveSession) {
	s.mu.Lock()
	s.liveByChat[chatID] = live
	s.mu.Unlock()
}

// createNewSession calls NewSession on the connection and updates live session fields.
// Caller holds sessionLock.
func (s *AcpAgentService) createNewSession(ctx context.Context, chatID int64, live *liveSession, targetWorkspace string) error {
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
	s.sessionHistory[chatID] = appendCappedSessionHistory(s.sessionHistory[chatID], SessionInfo{
		SessionID: live.sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()
	return nil
}

// ensureProcess 确保 chatID 有一个活跃的 ACP 进程。
// 如果已有进程则直接返回 liveSession；否则 spawn 新进程 + Initialize。
// Caller must hold sessionLock.
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

// NewSession 在现有 ACP 进程上创建新 session，无进程时先 spawn。
// 当请求的 workspace 与当前进程的 workspace 不同时，会杀掉旧进程并重新 spawn，
// 因为 ACP agent 使用进程的 cwd 而非 NewSession.Cwd 来决定工作目录。
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

	// ACP agents use process cwd (cmd.Dir) for filesystem operations, not NewSession.Cwd.
	// When workspace changes, respawn the process so cmd.Dir matches the target workspace.
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

// LoadSession 在现有 ACP 进程上加载已有 session。
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
		return err
	}

	s.mu.Lock()
	live.sessionID = sessionID
	live.workspace = targetWorkspace
	s.sessionHistory[chatID] = appendCappedSessionHistory(s.sessionHistory[chatID], SessionInfo{
		SessionID: sessionID,
		Workspace: targetWorkspace,
		UpdatedAt: time.Now(),
	})
	s.mu.Unlock()

	return nil
}

// ListSessions 在现有 ACP 进程上调 session/list。
// 如果 agent 支持 session/list 则使用远程结果，否则 fallback 到本地 sessionHistory。
func (s *AcpAgentService) ListSessions(ctx context.Context, chatID int64) ([]SessionInfo, error) {
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
		result := make([]SessionInfo, 0, len(items))
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
	result := make([]SessionInfo, len(history))
	copy(result, history)
	return result, nil
}

// Reconnect 杀掉 ACP 进程并重新 spawn + new_session。
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

// ErrNoActiveProcess is returned when there is no active ACP process for the chat.
var ErrNoActiveProcess = errors.New("no active ACP process")

// fileURIResult is the typed result of resolveFileURI.
type fileURIResult struct {
	data        []byte
	name        string
	warning     string
	passThrough bool // true when the file should be forwarded as-is (non-local URI)
}

func extractFileURI(f FileData) string {
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

// resolveFileURI resolves a file:// URI in f to actual file content.
// Returns passThrough=true for non-local URIs that should be forwarded unchanged.
// Returns a non-empty warning when the file cannot be read (caller appends to reply text).
// Returns data+name on success.
func resolveFileURI(f FileData, workspaceAbs string) fileURIResult {
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

// ResolveFileURIResources resolves file:// URIs in reply files to actual file content.
func (s *AcpAgentService) ResolveFileURIResources(reply *AgentReply, workspace string) *AgentReply {
	if reply == nil {
		return nil
	}
	out := &AgentReply{
		Text:       reply.Text,
		Images:     append([]ImageData(nil), reply.Images...),
		Files:      nil,
		Activities: append([]ActivityBlock(nil), reply.Activities...),
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		workspaceAbs = filepath.Clean(workspace)
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
			out.Images = append(out.Images, ImageData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		default:
			out.Files = append(out.Files, FileData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		}
	}
	return out
}

// Prompt sends a prompt to the agent and returns the reply.
func (s *AcpAgentService) Prompt(ctx context.Context, chatID int64, input PromptInput) (*AgentReply, error) {
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
		"text", util.LogTextPreview(input.Text, 200),
	)

	permMode := live.permMode
	logEvents := shouldLogEventOutput(s.cfg.EventOutput)

	live.client.SetCallbacks(
		func(b ActivityBlock) {
			if logEvents {
				slog.Info("ACP activity event",
					"chat_id", chatID,
					"session_id", live.sessionID,
					"kind", b.Kind,
					"status", b.Status,
					"detail", util.LogTextPreview(b.Detail, 200),
					"text", util.LogTextPreview(b.Text, 200),
				)
			}
			if onActivity != nil {
				onActivity(chatID, b)
			}
		},
		func(req PermissionRequest) <-chan PermissionResponse {
			if logEvents {
				slog.Info("ACP permission event",
					"chat_id", chatID,
					"session_id", live.sessionID,
					"request_id", req.ID,
					"tool", util.LogTextPreview(req.Tool, 200),
				)
			}
			ch := make(chan PermissionResponse, 1)
			switch permMode {
			case PermissionModeApprove:
				ch <- PermissionResponse{Decision: PermissionAlways}
				return ch
			case PermissionModeDeny:
				ch <- PermissionResponse{Decision: PermissionDeny}
				return ch
			default:
				if onPermission != nil {
					return onPermission(chatID, req)
				}
				ch <- PermissionResponse{Decision: PermissionDeny}
				return ch
			}
		},
	)

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

// Shutdown stops all active agent sessions and cancels the service context.
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

// SetSessionPermissionMode updates the permission mode for the given chat's live session.
func (s *AcpAgentService) SetSessionPermissionMode(chatID int64, mode PermissionMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.liveByChat[chatID]; live != nil {
		live.permMode = mode
	}
}

// slogWriter adapts slog to io.Writer for capturing subprocess stderr.
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

// BuildContentBlocks converts PromptInput to SDK ContentBlock slice.
func BuildContentBlocks(input PromptInput) []acpsdk.ContentBlock {
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
