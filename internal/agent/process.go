package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/acpclient"
	"github.com/zhu327/acpclaw/internal/domain"
)

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

// ensureProcess ensures an active ACP process for chatID. Caller must hold sessionLock.
func (s *AcpAgentService) ensureProcess(ctx context.Context, chatID string, workspace string) (*liveSession, error) {
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
