package acpclient

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

const (
	// terminalIdleTimeout is how long an exited terminal may sit idle (no Output/WaitForExit)
	// after exit before the reaper deletes it. Idle time is max(exitTime, lastAccess).
	terminalIdleTimeout = 5 * time.Minute
	reaperInterval      = 30 * time.Second
)

// terminal holds a running subprocess and its buffered output.
type terminal struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	outBuf     bytes.Buffer
	done       chan struct{}
	exitCode   *int
	exitSignal *string
	exitTime   time.Time // when cmd.Wait returned; zero while running
	lastAccess time.Time // last Output / WaitForExit call; zero until first access
	limit      int       // max output bytes to retain; 0 = unlimited
}

// touch records the current time as the last access.
func (t *terminal) touch() {
	t.mu.Lock()
	t.lastAccess = time.Now()
	t.mu.Unlock()
}

// idleSince is the later of exitTime and lastAccess once exited; zero while still running.
func (t *terminal) idleSince() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exitTime.IsZero() {
		return time.Time{}
	}
	if t.lastAccess.After(t.exitTime) {
		return t.lastAccess
	}
	return t.exitTime
}

func (t *terminal) appendOutput(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.outBuf.Write(p)
	if t.limit > 0 && t.outBuf.Len() > t.limit {
		excess := t.outBuf.Len() - t.limit
		t.outBuf.Next(excess)
	}
}

func (t *terminal) snapshot() (output string, truncated bool, exitStatus *acpsdk.TerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	output = t.outBuf.String()
	truncated = t.limit > 0 && t.outBuf.Len() >= t.limit
	if t.exitCode != nil || t.exitSignal != nil {
		exitStatus = &acpsdk.TerminalExitStatus{
			ExitCode: t.exitCode,
			Signal:   t.exitSignal,
		}
	}
	return
}

// killProcessGroup SIGKILLs the whole process group (Setpgid: true → kill negative pid).
func (t *terminal) killProcessGroup() {
	if t.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
}

// terminalWriter is an io.Writer that feeds bytes into a terminal's buffer.
type terminalWriter struct{ t *terminal }

func (w *terminalWriter) Write(p []byte) (int, error) {
	w.t.appendOutput(p)
	return len(p), nil
}

// TerminalManager manages per-session terminal subprocesses.
type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]map[string]*terminal // sessionID → terminalID → terminal
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewTerminalManager creates a new TerminalManager and starts a background
// reaper that removes terminated terminals after terminalIdleTimeout.
func NewTerminalManager() *TerminalManager {
	m := &TerminalManager{
		sessions: make(map[string]map[string]*terminal),
		stopCh:   make(chan struct{}),
	}
	go m.reapLoop()
	return m
}

// Stop shuts down the background reaper. Safe to call multiple times.
func (m *TerminalManager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
}

// reapLoop periodically removes terminals that have been idle (exited and
// not accessed) for longer than terminalIdleTimeout.
func (m *TerminalManager) reapLoop() {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.reapExpired()
		}
	}
}

func (m *TerminalManager) reapExpired() {
	now := time.Now()
	// Lock order: always m.mu before t.mu (via idleSince); never the reverse.
	m.mu.Lock()
	defer m.mu.Unlock()
	for sid, terms := range m.sessions {
		for tid, t := range terms {
			idle := t.idleSince()
			if idle.IsZero() || now.Sub(idle) <= terminalIdleTimeout {
				continue
			}
			slog.Debug("reaping idle terminal",
				"session_id", sid, "terminal_id", tid,
				"idle_for", now.Sub(idle).Round(time.Second))
			delete(terms, tid)
		}
		if len(terms) == 0 {
			delete(m.sessions, sid)
		}
	}
}

func (m *TerminalManager) sessionTerminals(sessionID string) map[string]*terminal {
	if m.sessions[sessionID] == nil {
		m.sessions[sessionID] = make(map[string]*terminal)
	}
	return m.sessions[sessionID]
}

// Create starts a new terminal subprocess and returns its ID.
func (m *TerminalManager) Create(req acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	args := req.Args
	if args == nil {
		args = []string{}
	}

	// Build env: inherit parent env, then overlay request env vars.
	var envPairs []string
	for _, e := range req.Env {
		envPairs = append(envPairs, e.Name+"="+e.Value)
	}

	cmd := exec.Command(req.Command, args...)
	if req.Cwd != nil && *req.Cwd != "" {
		cmd.Dir = *req.Cwd
	}
	if len(envPairs) > 0 {
		cmd.Env = append(cmd.Environ(), envPairs...)
	}

	limit := 0
	if req.OutputByteLimit != nil {
		limit = *req.OutputByteLimit
	}

	t := &terminal{
		done:  make(chan struct{}),
		limit: limit,
	}
	w := &terminalWriter{t: t}
	cmd.Stdout = w
	cmd.Stderr = w
	// Prevent terminal subprocesses from receiving signals sent to the bot process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.cmd = cmd

	if err := cmd.Start(); err != nil {
		return acpsdk.CreateTerminalResponse{}, fmt.Errorf("start terminal: %w", err)
	}

	terminalID := fmt.Sprintf("term-%d", cmd.Process.Pid)

	go func() {
		err := cmd.Wait()
		t.mu.Lock()
		code := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					if status.Signaled() {
						sig := status.Signal().String()
						t.exitSignal = &sig
					} else {
						c := status.ExitStatus()
						t.exitCode = &c
					}
				} else {
					c := exitErr.ExitCode()
					t.exitCode = &c
				}
			} else {
				c := 1
				t.exitCode = &c
			}
		} else {
			t.exitCode = &code
		}
		t.exitTime = time.Now()
		t.mu.Unlock()
		close(t.done)
	}()

	m.mu.Lock()
	m.sessionTerminals(string(req.SessionId))[terminalID] = t
	m.mu.Unlock()

	return acpsdk.CreateTerminalResponse{TerminalId: terminalID}, nil
}

// Output returns the current output and exit status of a terminal.
//
// Benign TOCTOU: get() then touch() can race with the reaper removing the map
// entry; the *terminal remains valid and the idle timeout makes this rare.
func (m *TerminalManager) Output(req acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	t := m.get(string(req.SessionId), req.TerminalId)
	if t == nil {
		return acpsdk.TerminalOutputResponse{}, fmt.Errorf("terminal not found: %s", req.TerminalId)
	}
	t.touch()
	output, truncated, exitStatus := t.snapshot()
	// TerminalOutputResponse.Output is required (non-empty per Validate), use a space if empty.
	if output == "" {
		output = " "
	}
	return acpsdk.TerminalOutputResponse{
		Output:     output,
		Truncated:  truncated,
		ExitStatus: exitStatus,
	}, nil
}

// WaitForExit blocks until the terminal exits or ctx is cancelled (same TOCTOU as Output).
func (m *TerminalManager) WaitForExit(
	ctx context.Context,
	req acpsdk.WaitForTerminalExitRequest,
) (acpsdk.WaitForTerminalExitResponse, error) {
	t := m.get(string(req.SessionId), req.TerminalId)
	if t == nil {
		return acpsdk.WaitForTerminalExitResponse{}, fmt.Errorf("terminal not found: %s", req.TerminalId)
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		return acpsdk.WaitForTerminalExitResponse{}, ctx.Err()
	}
	t.touch()
	t.mu.Lock()
	defer t.mu.Unlock()
	return acpsdk.WaitForTerminalExitResponse{
		ExitCode: t.exitCode,
		Signal:   t.exitSignal,
	}, nil
}

// Kill sends SIGKILL to the terminal process.
func (m *TerminalManager) Kill(req acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error) {
	t := m.get(string(req.SessionId), req.TerminalId)
	if t == nil {
		return acpsdk.KillTerminalCommandResponse{}, fmt.Errorf("terminal not found: %s", req.TerminalId)
	}
	t.killProcessGroup()
	return acpsdk.KillTerminalCommandResponse{}, nil
}

// Release removes the terminal record and kills the process if still running.
func (m *TerminalManager) Release(req acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	m.mu.Lock()
	sessions := m.sessions[string(req.SessionId)]
	var t *terminal
	if sessions != nil {
		t = sessions[req.TerminalId]
		delete(sessions, req.TerminalId)
	}
	m.mu.Unlock()

	if t != nil {
		select {
		case <-t.done:
		default:
			t.killProcessGroup()
		}
	}
	return acpsdk.ReleaseTerminalResponse{}, nil
}

// ReleaseSession kills and removes all terminals for a session.
func (m *TerminalManager) ReleaseSession(sessionID string) {
	m.mu.Lock()
	terminals := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	if len(terminals) > 0 {
		slog.Debug("releasing session terminals",
			"session_id", sessionID, "count", len(terminals))
	}
	for tid, t := range terminals {
		select {
		case <-t.done:
		default:
			slog.Debug("killing running terminal during session release",
				"session_id", sessionID, "terminal_id", tid)
			t.killProcessGroup()
		}
	}
}

func (m *TerminalManager) get(sessionID, terminalID string) *terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := m.sessions[sessionID]
	if sessions == nil {
		return nil
	}
	return sessions[terminalID]
}
