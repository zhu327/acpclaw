package acpclient_test

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/acpclient"
	"github.com/zhu327/acpclaw/internal/domain"
)

func TestActivityLabel(t *testing.T) {
	tests := []struct {
		kind     string
		toolName string
		want     string
	}{
		{"think", "", "💡 Thinking"},
		{"execute", "", "⚙️ Running"},
		{"read", "", "📖 Reading"},
		{"edit", "", "✏️ Editing"},
		{"write", "", "✍️ Writing"},
		{"search", "web_search", "🌐 Searching web"},
		{"search", "browser", "🌐 Searching web"},
		{"search", "local_search", "🔎 Querying project"},
		{"search", "code_search", "🔎 Querying project"},
		{"search", "grep", "🔎 Querying"},
		{"unknown", "", "⚙️ Running"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.toolName, func(t *testing.T) {
			assert.Equal(t, tt.want, acpclient.ActivityLabel(tt.kind, tt.toolName))
		})
	}
}

func TestInferActivityKind(t *testing.T) {
	tests := []struct {
		toolName string
		want     domain.ActivityKind
	}{
		{"think", domain.ActivityThink},
		{"read_file", domain.ActivityRead},
		{"view_file", domain.ActivityRead},
		{"edit_file", domain.ActivityEdit},
		{"str_replace", domain.ActivityEdit},
		{"write_file", domain.ActivityWrite},
		{"create_file", domain.ActivityWrite},
		{"web_search", domain.ActivitySearch},
		{"find_files", domain.ActivitySearch},
		{"bash", domain.ActivityExecute},
	}
	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			assert.Equal(t, tt.want, acpclient.InferActivityKind(tt.toolName))
		})
	}
}

func TestRequestPermission_ThisTimePrefersAllowOnce(t *testing.T) {
	title := "Run command"
	client := acpclient.NewAcpClient(nil, func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return ch
	})

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
			{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		},
		ToolCall: acpsdk.RequestPermissionToolCall{
			ToolCallId: "tool-1",
			Title:      &title,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	assert.Equal(t, acpsdk.PermissionOptionId("allow-once"), resp.Outcome.Selected.OptionId)
}

func TestRequestPermission_ThisTimeDeniesWhenNoAllowOnce(t *testing.T) {
	title := "Run command"
	client := acpclient.NewAcpClient(nil, func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: domain.PermissionThisTime}
		return ch
	})

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
			{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
		},
		ToolCall: acpsdk.RequestPermissionToolCall{
			ToolCallId: "tool-escalation",
			Title:      &title,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	assert.Equal(t, acpsdk.PermissionOptionId("reject-once"), resp.Outcome.Selected.OptionId,
		"PermissionThisTime must NOT escalate to AllowAlways when AllowOnce is unavailable")
}

func TestRequestPermission_UnknownDecisionDefaultsToDeny(t *testing.T) {
	title := "Run command"
	client := acpclient.NewAcpClient(nil, func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		ch <- domain.PermissionResponse{Decision: "invalid_value"}
		return ch
	})

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		Options: []acpsdk.PermissionOption{
			{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
			{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
		},
		ToolCall: acpsdk.RequestPermissionToolCall{
			ToolCallId: "tool-unknown",
			Title:      &title,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	assert.Equal(t, acpsdk.PermissionOptionId("reject-once"), resp.Outcome.Selected.OptionId,
		"Unknown decision must default to deny")
}

func TestRequestPermission_NoHandlerDefaultsToDeny(t *testing.T) {
	client := acpclient.NewAcpClient(nil, nil)

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		Options: []acpsdk.PermissionOption{
			{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
			{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		},
		ToolCall: acpsdk.RequestPermissionToolCall{
			ToolCallId: "tool-2",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Outcome.Selected)
	assert.Equal(t, acpsdk.PermissionOptionId("reject-once"), resp.Outcome.Selected.OptionId)
}

// Task 5: PermissionRequest must carry AvailableActions computed from SDK options (Python _available_actions parity).
func TestRequestPermission_AvailableActionsFromOptions(t *testing.T) {
	tests := []struct {
		name     string
		options  []acpsdk.PermissionOption
		wantActs []domain.PermissionDecision
	}{
		{
			name: "allow_always and allow_once -> always, once, deny",
			options: []acpsdk.PermissionOption{
				{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
				{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
			},
			wantActs: []domain.PermissionDecision{
				domain.PermissionAlways,
				domain.PermissionThisTime,
				domain.PermissionDeny,
			},
		},
		{
			name: "allow_always only -> always, deny",
			options: []acpsdk.PermissionOption{
				{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
				{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
			wantActs: []domain.PermissionDecision{domain.PermissionAlways, domain.PermissionDeny},
		},
		{
			name: "allow_once only -> once, deny",
			options: []acpsdk.PermissionOption{
				{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
			wantActs: []domain.PermissionDecision{domain.PermissionThisTime, domain.PermissionDeny},
		},
		{
			name: "deny only -> deny",
			options: []acpsdk.PermissionOption{
				{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
			wantActs: []domain.PermissionDecision{domain.PermissionDeny},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured domain.PermissionRequest
			client := acpclient.NewAcpClient(nil, func(req domain.PermissionRequest) <-chan domain.PermissionResponse {
				captured = req
				ch := make(chan domain.PermissionResponse, 1)
				ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}
				return ch
			})
			_, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
				Options:  tt.options,
				ToolCall: acpsdk.RequestPermissionToolCall{ToolCallId: "tc1"},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantActs, captured.AvailableActions,
				"AvailableActions must match SDK options (allow_always->always, allow_once->once, always deny)")
		})
	}
}

func TestSessionUpdate_TextChunks(t *testing.T) {
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	// Agent includes spaces in chunks; no auto-spacing (REVISED APPROACH)
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("hello "),
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("world"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "hello world", reply.Text)
}

func TestSessionUpdate_ToolCallActivity(t *testing.T) {
	var received []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		received = append(received, b)
	}, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "tc1",
				Kind:       acpsdk.ToolKindRead,
				Title:      "read_file",
			},
		},
	})
	require.NoError(t, err)
	require.Empty(t, received, "tool open no longer emits in_progress")

	status := acpsdk.ToolCallStatusCompleted
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "tc1",
				Status:     &status,
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Len(t, reply.Activities, 1)
	assert.False(t, reply.Activities[0].EndAt.IsZero())
}

func TestAppendText_PunctuationSpacing(t *testing.T) {
	// Auto-spacing: after sentence punctuation, insert space before alphanumeric. "Created (deleted)." + "Done." → "Created (deleted). Done."
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("Created (deleted)."),
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("Done."),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "Created (deleted). Done.", reply.Text)
}

func TestAppendText_NoSpaceInSemver(t *testing.T) {
	// "Version 10." + "1.2" → "Version 10.1.2" (no space in version numbers)
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("Version 10."),
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("1.2"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "Version 10.1.2", reply.Text)
}

func TestAppendText_NoSpaceMidWord(t *testing.T) {
	// "Sil" + "ence" → "Silence" (no space between mid-word chunks)
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("Sil"),
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("ence"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "Silence", reply.Text)
}

func TestClient_PendingNonToolTextFlushedAsThink(t *testing.T) {
	var activities []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		activities = append(activities, b)
	}, nil)
	client.StartCapture()

	// Send text without a tool block → pending non-tool text
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("first thought"),
			},
		},
	})
	require.NoError(t, err)

	// Open a tool block → should flush pending text as think
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "t1",
				Kind:       acpsdk.ToolKindExecute,
				Title:      "Run git log",
			},
		},
	})
	require.NoError(t, err)

	require.Len(t, activities, 1, "expect think block only; tool open no longer emits in_progress")
	assert.Equal(t, domain.ActivityThink, activities[0].Kind)
	assert.Equal(t, "first thought", activities[0].Text)
}

func TestClient_TrailingNonThinkBlockTextMovesToReply(t *testing.T) {
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	// Open execute block
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "t1",
				Kind:       acpsdk.ToolKindExecute,
				Title:      "Run git show",
			},
		},
	})
	require.NoError(t, err)

	// Send text while block is active → goes to activeBlockChunks
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("This should be final."),
			},
		},
	})
	require.NoError(t, err)

	// Leave the block active and finish capture: trailing non-think text should
	// move to reply text at prompt end.
	reply := client.FinishCapture()
	assert.Equal(t, "This should be final.", reply.Text)
	require.Len(t, reply.Activities, 1)
	assert.Equal(t, "", reply.Activities[0].Text, "trailing block text should move to reply, not stay in block")
}

func TestSessionUpdate_ImageContent(t *testing.T) {
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.ImageBlock("AQID", "image/png"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	require.Len(t, reply.Images, 1)
	assert.Equal(t, "image/png", reply.Images[0].MIMEType)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, reply.Images[0].Data)
}

func TestSessionUpdate_ToolCompletionEmitsClosedBlockWithText(t *testing.T) {
	var received []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		received = append(received, b)
	}, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "tc-complete",
				Kind:       acpsdk.ToolKindRead,
				Title:      "Read file",
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "tc-complete",
				Content: []acpsdk.ToolCallContent{
					{
						Content: &acpsdk.ToolCallContentContent{
							Content: acpsdk.TextBlock("hello from tool"),
							Type:    "content",
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	status := acpsdk.ToolCallStatusCompleted
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "tc-complete",
				Status:     &status,
			},
		},
	})
	require.NoError(t, err)

	require.Len(t, received, 1, "expect completed activity callback only")
	assert.Equal(t, "completed", received[0].Status)
	assert.Equal(t, "hello from tool", received[0].Text)
}

func TestSessionUpdate_ToolFailureEmitsClosedBlock(t *testing.T) {
	var received []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		received = append(received, b)
	}, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "tc-fail",
				Kind:       acpsdk.ToolKindExecute,
				Title:      "Run command",
			},
		},
	})
	require.NoError(t, err)

	status := acpsdk.ToolCallStatusFailed
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "tc-fail",
				Status:     &status,
			},
		},
	})
	require.NoError(t, err)

	require.Len(t, received, 1, "expect failed activity callback only")
	assert.Equal(t, "failed", received[0].Status)
	assert.Equal(t, domain.ActivityExecute, received[0].Kind)
}

// Task 6 parity: Python does NOT emit a think block at prompt end for trailing pending non-tool text.
// The text goes directly to reply.Text. Only flush-as-think happens when a tool block opens (before the tool).
func TestFinishCapture_NoThinkBlockForTrailingPendingNonToolText(t *testing.T) {
	var activities []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		activities = append(activities, b)
	}, nil)
	client.StartCapture()

	// Only pending non-tool text, no tool blocks
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("direct reply to user"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "direct reply to user", reply.Text)
	assert.Empty(t, activities, "Python: no think block emitted at prompt end for trailing non-tool text")
	assert.Empty(t, reply.Activities, "reply should not contain a think block for trailing text")
}

// Task 6 parity: Empty/whitespace-only pending non-tool text should not emit a think block when flushed before tool.
func TestSessionUpdate_DropsEmptyPendingNonToolTextWhenFlushingBeforeTool(t *testing.T) {
	var activities []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		activities = append(activities, b)
	}, nil)
	client.StartCapture()

	// Send whitespace-only chunks (simulates partial/empty thinking)
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("   "),
			},
		},
	})
	require.NoError(t, err)

	// Open tool block -> should flush pending; empty text must not emit think
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "t1",
				Kind:       acpsdk.ToolKindExecute,
				Title:      "Run cmd",
			},
		},
	})
	require.NoError(t, err)

	require.Empty(t, activities, "no think block for empty pending; tool open no longer emits in_progress")
}

// Task 6 parity: Think block with text then completed -> reply.activity_blocks contains completed think, reply.text gets post-tool text.
func TestSessionUpdate_ThinkBlockCompletedThenPostToolTextInReply(t *testing.T) {
	client := acpclient.NewAcpClient(nil, nil)
	client.StartCapture()

	// Think block
	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "think-1",
				Kind:       acpsdk.ToolKindThink,
				Title:      "thinking step",
			},
		},
	})
	require.NoError(t, err)

	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("draft plan"),
			},
		},
	})
	require.NoError(t, err)

	status := acpsdk.ToolCallStatusCompleted
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "think-1",
				Status:     &status,
			},
		},
	})
	require.NoError(t, err)

	// Post-tool text goes to reply
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content: acpsdk.TextBlock("final answer"),
			},
		},
	})
	require.NoError(t, err)

	reply := client.FinishCapture()
	assert.Equal(t, "final answer", reply.Text)
	require.Len(t, reply.Activities, 1)
	assert.Equal(t, domain.ActivityThink, reply.Activities[0].Kind)
	assert.Equal(t, "completed", reply.Activities[0].Status)
	assert.Equal(t, "draft plan", reply.Activities[0].Text)
}

func TestSessionUpdate_CompletedToolWithNoTextEmitsActivity(t *testing.T) {
	var received []domain.ActivityBlock
	client := acpclient.NewAcpClient(func(b domain.ActivityBlock) {
		received = append(received, b)
	}, nil)
	client.StartCapture()

	err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "tc-no-text",
				Kind:       acpsdk.ToolKindEdit,
				Title:      "Edit file.go",
			},
		},
	})
	require.NoError(t, err)

	status := acpsdk.ToolCallStatusCompleted
	err = client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: "tc-no-text",
				Status:     &status,
			},
		},
	})
	require.NoError(t, err)

	require.Len(t, received, 1, "completed tool with no text should still emit activity")
	assert.Equal(t, "completed", received[0].Status)
	assert.Equal(t, domain.ActivityEdit, received[0].Kind)
	assert.Equal(t, "", received[0].Text)
}
