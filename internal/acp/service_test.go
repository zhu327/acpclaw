package acp_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/acp"
)

func TestAgentService_ActiveSessionNone(t *testing.T) {
	svc := acp.NewAgentService(acp.ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})
	assert.Nil(t, svc.ActiveSession(999))
}

func TestAgentService_Stop_NoSession(t *testing.T) {
	svc := acp.NewAgentService(acp.ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})
	err := svc.Stop(context.Background(), 42)
	assert.ErrorIs(t, err, acp.ErrNoActiveSession)
}

func TestAgentService_Cancel_NoSession(t *testing.T) {
	svc := acp.NewAgentService(acp.ServiceConfig{
		AgentCommand:   []string{"echo"},
		ConnectTimeout: time.Second,
	})
	err := svc.Cancel(context.Background(), 42)
	assert.ErrorIs(t, err, acp.ErrNoActiveSession)
}

func TestAgentService_SetHandlers(t *testing.T) {
	svc := acp.NewAgentService(acp.ServiceConfig{AgentCommand: []string{"echo"}})
	var activityCalled bool
	svc.SetActivityHandler(func(chatID int64, block acp.ActivityBlock) {
		activityCalled = true
	})
	var permCalled bool
	svc.SetPermissionHandler(func(chatID int64, req acp.PermissionRequest) <-chan acp.PermissionResponse {
		permCalled = true
		ch := make(chan acp.PermissionResponse, 1)
		ch <- acp.PermissionResponse{Decision: acp.PermissionThisTime}
		return ch
	})
	assert.False(t, activityCalled)
	assert.False(t, permCalled)
}

func TestAgentService_ListResumableSessions_NoSession(t *testing.T) {
	svc := acp.NewAgentService(acp.ServiceConfig{AgentCommand: []string{"echo"}})
	sessions, err := svc.ListResumableSessions(context.Background(), 42)
	assert.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestBuildContentBlocks_TextOnly(t *testing.T) {
	input := acp.PromptInput{Text: "hello"}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "hello", blocks[0].Text.Text)
}

func TestBuildContentBlocks_WithImage(t *testing.T) {
	input := acp.PromptInput{
		Text: "check this",
		Images: []acp.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	assert.Len(t, blocks, 2)
	assert.NotNil(t, blocks[0].Text)
	assert.NotNil(t, blocks[1].Image)
}

// TestBuildContentBlocks_TextFileSemantic verifies text file (TextContent != nil) emits text block:
// File: <name>\n\n<content> (Python parity)
func TestBuildContentBlocks_TextFileSemantic(t *testing.T) {
	content := "hello from file"
	input := acp.PromptInput{
		Files: []acp.FileData{
			{
				MIMEType:    "text/plain",
				Name:        "readme.txt",
				TextContent: &content,
			},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "File: readme.txt\n\nhello from file", blocks[0].Text.Text)
}

// TestBuildContentBlocks_BinaryFileSemantic verifies binary file (TextContent == nil) emits text block:
// Binary file attached: <name> (<mime>) (Python parity)
func TestBuildContentBlocks_BinaryFileSemantic(t *testing.T) {
	input := acp.PromptInput{
		Files: []acp.FileData{
			{
				MIMEType: "application/octet-stream",
				Data:     []byte{0x00, 0x01, 0x02},
				Name:     "bin.dat",
			},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "Binary file attached: bin.dat (application/octet-stream)", blocks[0].Text.Text)
}

// TestBuildContentBlocks_BinaryFileEmptyMime verifies MIME fallback matches Python (unknown when empty).
func TestBuildContentBlocks_BinaryFileEmptyMime(t *testing.T) {
	input := acp.PromptInput{
		Files: []acp.FileData{
			{MIMEType: "", Name: "data.bin"},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "Binary file attached: data.bin (unknown)", blocks[0].Text.Text)
}

// TestBuildContentBlocks_ImageRemainsImageBlock verifies image input still produces image block.
func TestBuildContentBlocks_ImageRemainsImageBlock(t *testing.T) {
	input := acp.PromptInput{
		Images: []acp.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 1)
	assert.NotNil(t, blocks[0].Image)
}

// TestBuildContentBlocks_CompositionOrder verifies block order: text -> image -> file (text block).
func TestBuildContentBlocks_CompositionOrder(t *testing.T) {
	fileContent := "file body"
	input := acp.PromptInput{
		Text: "main text",
		Images: []acp.ImageData{
			{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		},
		Files: []acp.FileData{
			{Name: "doc.txt", TextContent: &fileContent},
		},
	}
	blocks := acp.BuildContentBlocks(input)
	require.Len(t, blocks, 3)
	require.NotNil(t, blocks[0].Text)
	assert.Equal(t, "main text", blocks[0].Text.Text)
	assert.NotNil(t, blocks[1].Image)
	require.NotNil(t, blocks[2].Text)
	assert.Equal(t, "File: doc.txt\n\nfile body", blocks[2].Text.Text)
}
