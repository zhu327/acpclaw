package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/builtin/agent"
	"github.com/zhu327/acpclaw/internal/domain"
)

func TestAgentService_ActiveSessionNone(t *testing.T) {
	svc := agent.NewAgentService(agent.ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})
	assert.Nil(t, svc.ActiveSession("999"))
}

func TestAgentService_Cancel_NoSession(t *testing.T) {
	svc := agent.NewAgentService(agent.ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})
	err := svc.Cancel(context.Background(), "42")
	assert.ErrorIs(t, err, domain.ErrNoActiveSession)
}

func TestAgentService_SetHandlers(t *testing.T) {
	svc := agent.NewAgentService(agent.ServiceConfig{AgentCommand: []string{"echo"}})
	var activityCalled bool
	svc.SetActivityHandler(func(chatID string, block domain.ActivityBlock) {
		activityCalled = true
	})
	var permCalled bool
	svc.SetPermissionHandler(func(chatID string, req domain.PermissionRequest) <-chan domain.PermissionResponse {
		permCalled = true
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return ch
	})
	assert.False(t, activityCalled)
	assert.False(t, permCalled)
}

func TestAgentService_ListSessions_NoProcess(t *testing.T) {
	svc := agent.NewAgentService(agent.ServiceConfig{AgentCommand: []string{"echo"}})
	sessions, err := svc.ListSessions(context.Background(), "42")
	assert.ErrorIs(t, err, domain.ErrNoActiveProcess)
	assert.Nil(t, sessions)
}

func TestBuildContentBlocks_TextOnly(t *testing.T) {
	input := domain.PromptInput{Text: "hello"}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "hello", blocks[0].Text.Text)
}

func TestBuildContentBlocks_WithImage(t *testing.T) {
	input := domain.PromptInput{
		Text: "check this",
		Images: []domain.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	assert.Len(t, blocks, 2)
	assert.NotNil(t, blocks[0].Text)
	assert.NotNil(t, blocks[1].Image)
}

// TestBuildContentBlocks_TextFileSemantic verifies text file (TextContent != nil) emits text block:
// File: <name>\n\n<content> (Python parity)
func TestBuildContentBlocks_TextFileSemantic(t *testing.T) {
	content := "hello from file"
	input := domain.PromptInput{
		Files: []domain.FileData{
			{
				MIMEType:    "text/plain",
				Name:        "readme.txt",
				TextContent: &content,
			},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "File: readme.txt\n\nhello from file", blocks[0].Text.Text)
}

// TestBuildContentBlocks_BinaryFileSemantic verifies binary file (TextContent == nil) emits text block:
// Binary file attached: <name> (<mime>) (Python parity)
func TestBuildContentBlocks_BinaryFileSemantic(t *testing.T) {
	input := domain.PromptInput{
		Files: []domain.FileData{
			{
				MIMEType: "application/octet-stream",
				Data:     []byte{0x00, 0x01, 0x02},
				Name:     "bin.dat",
			},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "Binary file attached: bin.dat (application/octet-stream)", blocks[0].Text.Text)
}

// TestBuildContentBlocks_BinaryFileEmptyMime verifies MIME fallback matches Python (unknown when empty).
func TestBuildContentBlocks_BinaryFileEmptyMime(t *testing.T) {
	input := domain.PromptInput{
		Files: []domain.FileData{
			{MIMEType: "", Name: "data.bin"},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "Binary file attached: data.bin (unknown)", blocks[0].Text.Text)
}

// TestBuildContentBlocks_ImageRemainsImageBlock verifies image input still produces image block.
func TestBuildContentBlocks_ImageRemainsImageBlock(t *testing.T) {
	input := domain.PromptInput{
		Images: []domain.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	assert.NotNil(t, blocks[0].Image)
}

// TestBuildContentBlocks_CompositionOrder verifies block order: text -> image -> file (text block).
func TestBuildContentBlocks_CompositionOrder(t *testing.T) {
	fileContent := "file body"
	input := domain.PromptInput{
		Text: "main text",
		Images: []domain.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
		Files: []domain.FileData{
			{Name: "doc.txt", TextContent: &fileContent},
		},
	}
	blocks := agent.BuildContentBlocks(input)
	require.Len(t, blocks, 3)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "main text", blocks[0].Text.Text)
	assert.NotNil(t, blocks[1].Image)
	require.NotNil(t, blocks[2].Text)
	assert.Equal(t, "File: doc.txt\n\nfile body", blocks[2].Text.Text)
}
