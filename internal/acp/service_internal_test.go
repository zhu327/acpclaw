package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopLiveSession_GracefulShutdown(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())

	live := &liveSession{cmd: cmd}
	start := time.Now()
	stopLiveSession(live)
	elapsed := time.Since(start)

	assert.NotNil(t, cmd.ProcessState, "process should have exited")
	assert.True(t, elapsed < 5*time.Second, "should exit quickly after SIGTERM, not wait full 3s; elapsed=%v", elapsed)
}

func TestStopLiveSession_ForcesKillAfterTimeout(t *testing.T) {
	cmd := exec.Command("bash", "-c", "trap '' TERM; sleep 30")
	require.NoError(t, cmd.Start())

	// Give bash time to set the trap before we send SIGTERM
	time.Sleep(100 * time.Millisecond)

	live := &liveSession{cmd: cmd}
	start := time.Now()
	stopLiveSession(live)
	elapsed := time.Since(start)

	assert.NotNil(t, cmd.ProcessState, "process should have exited")
	assert.True(t, elapsed >= 3*time.Second, "should wait at least 3s before SIGKILL; elapsed=%v", elapsed)
}

func TestNewSession_CreatesWorkspaceDir(t *testing.T) {
	tmpDir := t.TempDir()
	workspace := filepath.Join(tmpDir, "sub", "workspace")

	svc := NewAgentService(ServiceConfig{
		AgentCommand:   []string{"nonexistent-command-xyz"},
		ConnectTimeout: time.Second,
	})

	err := svc.NewSession(context.Background(), 42, workspace)
	assert.Error(t, err) // spawn will fail

	info, statErr := os.Stat(workspace)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir(), "workspace directory should have been created")
}

func TestNewSession_RejectsFileAsWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "notadir")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))

	svc := NewAgentService(ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})

	err := svc.NewSession(context.Background(), 42, filePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestResolveFileURIResources_ImageFile(t *testing.T) {
	workspace := t.TempDir()
	imgPath := filepath.Join(workspace, "test.png")
	imgData := []byte{0x89, 0x50, 0x4E, 0x47}
	require.NoError(t, os.WriteFile(imgPath, imgData, 0o644))

	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "file://" + imgPath, MIMEType: "image/png"},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	require.Len(t, result.Images, 1)
	assert.Equal(t, "image/png", result.Images[0].MIMEType)
	assert.Equal(t, imgData, result.Images[0].Data)
	assert.Equal(t, "test.png", result.Images[0].Name)
	assert.Empty(t, result.Files)
}

func TestResolveFileURIResources_TextFile(t *testing.T) {
	workspace := t.TempDir()
	txtPath := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(txtPath, []byte("hello"), 0o644))

	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "file://" + txtPath, MIMEType: "text/plain"},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	require.Len(t, result.Files, 1)
	assert.Equal(t, "notes.txt", result.Files[0].Name)
	assert.Equal(t, []byte("hello"), result.Files[0].Data)
	assert.Equal(t, "text/plain", result.Files[0].MIMEType)
}

func TestResolveFileURIResources_OutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("secret"), 0o644))

	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "file://" + outsidePath, MIMEType: "text/plain"},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	assert.Empty(t, result.Files)
	assert.Contains(t, result.Text, "outside")
}

func TestResolveFileURIResources_NonFileURI(t *testing.T) {
	workspace := t.TempDir()
	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "https://example.com/file.txt", MIMEType: "text/plain", Data: []byte("remote")},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	require.Len(t, result.Files, 1)
	assert.Equal(t, "https://example.com/file.txt", result.Files[0].Name)
	assert.Equal(t, []byte("remote"), result.Files[0].Data)
}

func TestResolveFileURIResources_MissingFile(t *testing.T) {
	workspace := t.TempDir()
	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "file:///nonexistent/path.txt", MIMEType: "text/plain"},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	assert.Empty(t, result.Files)
	assert.Contains(t, result.Text, "Attachment warning")
	assert.Contains(t, result.Text, "path.txt")
}

// TestExtractRawConn verifies that the unsafe pointer cast in extractRawConn
// correctly extracts a non-nil *acpsdk.Connection from a ClientSideConnection.
// This test must be re-run after any SDK upgrade to catch layout changes early.
// SDK version under test: v0.6.3 (see go.mod).
func TestExtractRawConn(t *testing.T) {
	// Build a minimal ClientSideConnection using real (but discarded) pipes.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	client := NewAcpClient(nil, nil)
	csc := acpsdk.NewClientSideConnection(client, w, r)
	require.NotNil(t, csc)

	conn := extractRawConn(csc)
	assert.NotNil(t, conn, "extractRawConn must return a non-nil *Connection; "+
		"if this fails after an SDK upgrade, the ClientSideConnection memory layout has changed")
}

// TestBuildAgentEnv verifies that the env allowlist excludes secrets present in the
// parent environment and only passes through explicitly allowed variable names.
func TestBuildAgentEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret-token")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/root")

	svc := NewAgentService(ServiceConfig{})
	env := svc.buildAgentEnv()

	envMap := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	assert.NotContains(t, envMap, "TELEGRAM_BOT_TOKEN", "bot token must not be passed to agent subprocess")
	assert.Equal(t, "/usr/bin:/bin", envMap["PATH"])
	assert.Equal(t, "/root", envMap["HOME"])
}

// TestBuildAgentEnv_CustomAllowlist verifies that a custom AgentEnv allowlist is respected.
func TestBuildAgentEnv_CustomAllowlist(t *testing.T) {
	t.Setenv("CUSTOM_VAR", "custom-value")
	t.Setenv("OTHER_VAR", "other-value")

	svc := NewAgentService(ServiceConfig{
		AgentEnv: []string{"CUSTOM_VAR"},
	})
	env := svc.buildAgentEnv()

	envMap := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	assert.Equal(t, "custom-value", envMap["CUSTOM_VAR"])
	assert.NotContains(t, envMap, "OTHER_VAR")
}

func TestResolveFileURIResources_DirectoryPath(t *testing.T) {
	workspace := t.TempDir()
	subDir := filepath.Join(workspace, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	svc := NewAgentService(ServiceConfig{})
	reply := &AgentReply{
		Files: []FileData{
			{Name: "file://" + subDir, MIMEType: "text/plain"},
		},
	}
	result := svc.ResolveFileURIResources(reply, workspace)

	assert.Empty(t, result.Files)
	assert.Contains(t, result.Text, "directory, not a file")
}

func TestIsProcessAlive(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		stopLiveSession(&liveSession{cmd: cmd})
	})

	assert.True(t, isProcessAlive(cmd.Process), "running process should be alive")

	stopLiveSession(&liveSession{cmd: cmd})
	assert.False(t, isProcessAlive(cmd.Process), "stopped process should be reported dead")
}

func TestResolveSessionWorkspace(t *testing.T) {
	base := t.TempDir()
	svc := NewAgentService(ServiceConfig{
		AgentCommand: []string{"echo"},
		Workspace:    base,
	})

	current := filepath.Join(base, "current")
	require.NoError(t, os.MkdirAll(current, 0o755))

	ws, err := svc.resolveSessionWorkspace(current, "")
	require.NoError(t, err)
	assert.Equal(t, current, ws, "empty requested workspace should keep current workspace")

	requested := filepath.Join(base, "next")
	ws, err = svc.resolveSessionWorkspace(current, requested)
	require.NoError(t, err)
	assert.Equal(t, requested, ws, "requested workspace should be applied")

	info, statErr := os.Stat(requested)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir(), "requested workspace directory should exist")
}
