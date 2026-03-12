package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/acp"
)

// mockAgentService implements acp.AgentService for testing.
type mockAgentService struct {
	mu                 sync.Mutex
	newSessionErr      error
	loadSessionErr     error
	listSessionsResult []acp.SessionInfo
	listSessionsErr    error
	promptReply        *acp.AgentReply
	promptErr          error
	cancelErr          error
	reconnectErr       error
	activeSession      *acp.SessionInfo
	onActivityCalled   bool
	onPermissionCalled bool
	promptCalled       bool
	lastPromptInput    *acp.PromptInput
}

func (m *mockAgentService) NewSession(_ context.Context, _ int64, _ string) error {
	return m.newSessionErr
}

func (m *mockAgentService) LoadSession(_ context.Context, _ int64, _ string, _ string) error {
	return m.loadSessionErr
}

func (m *mockAgentService) ListSessions(_ context.Context, _ int64) ([]acp.SessionInfo, error) {
	return m.listSessionsResult, m.listSessionsErr
}

func (m *mockAgentService) Prompt(_ context.Context, _ int64, input acp.PromptInput) (*acp.AgentReply, error) {
	m.mu.Lock()
	m.promptCalled = true
	m.lastPromptInput = &input
	reply, err := m.promptReply, m.promptErr
	m.mu.Unlock()
	return reply, err
}

func (m *mockAgentService) Cancel(_ context.Context, _ int64) error {
	return m.cancelErr
}

func (m *mockAgentService) Reconnect(_ context.Context, _ int64, _ string) error {
	return m.reconnectErr
}

func (m *mockAgentService) ActiveSession(_ int64) *acp.SessionInfo {
	return m.activeSession
}

func (m *mockAgentService) Shutdown() {}

func (m *mockAgentService) SetActivityHandler(_ func(chatID int64, block acp.ActivityBlock)) {
	m.onActivityCalled = true
}

func (m *mockAgentService) SetPermissionHandler(
	_ func(chatID int64, req acp.PermissionRequest) <-chan acp.PermissionResponse,
) {
	m.onPermissionCalled = true
}

func (m *mockAgentService) SetSessionPermissionMode(_ int64, _ acp.PermissionMode) {}

func (m *mockAgentService) getPromptState() (bool, *acp.PromptInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.promptCalled, m.lastPromptInput
}

// blockingMockAgentService blocks Prompt until Unblock() or Cancel() is called.
// Cancel() sends to cancelUnblock so Prompt returns with error (simulates agent responding to cancel).
type blockingMockAgentService struct {
	mockAgentService
	promptEntered chan struct{}
	promptUnblock chan struct{}
	cancelUnblock chan struct{}
	promptCount   int
	promptMu      sync.Mutex
}

func (m *blockingMockAgentService) Prompt(
	ctx context.Context,
	chatID int64,
	input acp.PromptInput,
) (*acp.AgentReply, error) {
	m.promptMu.Lock()
	m.promptCount++
	m.promptMu.Unlock()
	m.mu.Lock()
	m.lastPromptInput = &input
	m.mu.Unlock()
	select {
	case m.promptEntered <- struct{}{}:
	default:
	}
	select {
	case <-m.promptUnblock:
		return &acp.AgentReply{Text: "ok"}, nil
	case <-m.cancelUnblock:
		return nil, errors.New("cancelled")
	}
}

func (m *blockingMockAgentService) Cancel(ctx context.Context, chatID int64) error {
	err := m.mockAgentService.Cancel(ctx, chatID)
	if err != nil {
		return err
	}
	select {
	case m.cancelUnblock <- struct{}{}:
	default:
	}
	return nil
}

func (m *blockingMockAgentService) Unblock() {
	m.promptUnblock <- struct{}{}
}

func newBlockingMock() *blockingMockAgentService {
	return &blockingMockAgentService{
		mockAgentService: mockAgentService{
			activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."},
		},
		promptEntered: make(chan struct{}, 1),
		promptUnblock: make(chan struct{}, 1),
		cancelUnblock: make(chan struct{}, 1),
	}
}

func TestBridge_BusyQueue_StoresAndDrains(t *testing.T) {
	mock := newBlockingMock()
	b := NewBridge(nil, mock, Config{}) // nil bot to avoid SendMessage in queueBusyPrompt
	chatID := int64(456)
	ctx := context.Background()

	// Goroutine 1: holds lock, runs drain loop with first input
	lock := b.chatMutex(chatID)
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer lock.Unlock()
		defer close(done)
		b.runPromptLoop(ctx, chatID, acp.PromptInput{Text: "first"})
	}()

	// Wait until first Prompt has been entered
	select {
	case <-mock.promptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Prompt did not block")
	}

	// Simulate second message: queue the pending (lock is held by goroutine 1)
	b.queueBusyPrompt(ctx, chatID, acp.PromptInput{Text: "second"}, 0)

	// Verify pending is stored
	b.pendingMu.Lock()
	p := b.pendingByChat[chatID]
	b.pendingMu.Unlock()
	require.NotNil(t, p, "pending should be stored")
	assert.Equal(t, "second", p.input.Text)

	// Unblock first Prompt; drain loop will pop pending and call Prompt again
	mock.Unblock()
	// Second Prompt also blocks; unblock it so drain loop can finish
	mock.Unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPromptLoop did not finish")
	}
	assert.Equal(t, 2, mock.promptCount, "both prompts should be processed")
}

func TestBridge_PopPending(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{})
	chatID := int64(999)
	input := acp.PromptInput{Text: "queued"}

	b.pendingMu.Lock()
	b.pendingByChat[chatID] = &pendingPrompt{input: input, chatID: chatID, token: "t1"}
	b.pendingMu.Unlock()

	p := b.popPending(chatID)
	require.NotNil(t, p)
	assert.Equal(t, "queued", p.input.Text)
	assert.Equal(t, chatID, p.chatID)

	p2 := b.popPending(chatID)
	assert.Nil(t, p2, "pending should be cleared after pop")
}

func TestBridge_ChatMutex_SameForSameChat(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{})

	m1 := b.chatMutex(123)
	m2 := b.chatMutex(123)
	assert.True(t, m1 == m2, "chatMutex should return same mutex for same chatID")

	m3 := b.chatMutex(456)
	assert.True(t, m1 != m3, "chatMutex should return different mutex for different chatID")
}

func TestBridge_IsAllowed_AllowAll(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{})
	assert.True(t, b.IsAllowed(123, "user"))
	assert.True(t, b.IsAllowed(0, ""))
}

func TestBridge_IsAllowed_ByID(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{
		AllowedUserIDs: []int64{100, 200},
	})
	assert.True(t, b.IsAllowed(100, ""))
	assert.True(t, b.IsAllowed(200, "other"))
	assert.False(t, b.IsAllowed(99, ""))
	assert.False(t, b.IsAllowed(300, ""))
}

func TestBridge_IsAllowed_ByUsername(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{
		AllowedUsernames: []string{"alice", "bob"},
	})
	assert.True(t, b.IsAllowed(0, "alice"))
	assert.True(t, b.IsAllowed(0, "bob"))
	assert.False(t, b.IsAllowed(0, "charlie"))
	assert.False(t, b.IsAllowed(0, ""))
}

func TestBridge_IsAllowed_CaseInsensitive(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{
		AllowedUsernames: []string{"Alice"},
	})
	assert.True(t, b.IsAllowed(0, "alice"))
	assert.True(t, b.IsAllowed(0, "ALICE"))
	assert.True(t, b.IsAllowed(0, "Alice"))
}

func TestBridge_RespondPermission_DropsIfNoPending(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{})
	require.NotPanics(t, func() {
		b.RespondPermission("nonexistent-req-id", acp.PermissionDeny)
	})
}

func TestBridge_RespondPermission_SendsToPending(t *testing.T) {
	b := NewBridge(nil, &mockAgentService{}, Config{})
	ch := make(chan acp.PermissionResponse, 1)
	b.pendingPerms = map[string]chan acp.PermissionResponse{"req1": ch}
	b.RespondPermission("req1", acp.PermissionThisTime)

	resp := <-ch
	assert.Equal(t, acp.PermissionThisTime, resp.Decision)
}

func TestIsCommandToSkip(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{"nil message", nil, false},
		{"empty text", &telego.Message{Text: ""}, false},
		{"command", &telego.Message{Text: "/help"}, true},
		{"command with args", &telego.Message{Text: "/new workspace"}, true},
		{"leading space then command", &telego.Message{Text: "  /start"}, true},
		{"plain text", &telego.Message{Text: "hello"}, false},
		{"text with slash", &telego.Message{Text: "see https://example.com"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCommandToSkip(tt.msg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildResumeKeyboard(t *testing.T) {
	sessions := []acp.SessionInfo{
		{SessionID: "s1", Workspace: ".", Title: "My Session"},
		{SessionID: "session-two", Workspace: "ws"},
		{
			SessionID: strings.Repeat("x", 50),
			Workspace: ".",
			Title:     "Long title that should be truncated properly here",
		},
	}
	kb := buildResumeKeyboard(sessions)
	require.NotNil(t, kb)
	require.Len(t, kb.InlineKeyboard, 3, "should have 3 rows")

	// First row: "1. My Session" (1-based, matches /session display)
	assert.Equal(t, "1. My Session", kb.InlineKeyboard[0][0].Text)
	assert.Equal(t, "resume|0", kb.InlineKeyboard[0][0].CallbackData)

	// Second row: "2. ws" (falls back to workspace, then session ID)
	assert.Equal(t, "2. ws", kb.InlineKeyboard[1][0].Text)
	assert.Equal(t, "resume|1", kb.InlineKeyboard[1][0].CallbackData)

	// Third row: truncated to 48 chars
	assert.Len(t, kb.InlineKeyboard[2][0].Text, 48)
	assert.Equal(t, "resume|2", kb.InlineKeyboard[2][0].CallbackData)

	// More than 10 sessions: only first 10 shown
	many := make([]acp.SessionInfo, 15)
	for i := range many {
		many[i] = acp.SessionInfo{SessionID: fmt.Sprintf("s%d", i+1), Workspace: "."}
	}
	kbMany := buildResumeKeyboard(many)
	require.Len(t, kbMany.InlineKeyboard, 10)
}

func TestPermissionDecisionLabels(t *testing.T) {
	// Parity: labels must match Python exactly
	assert.Equal(t, "Approved for this session.", permissionDecisionLabel(acp.PermissionAlways))
	assert.Equal(t, "Approved this time.", permissionDecisionLabel(acp.PermissionThisTime))
	assert.Equal(t, "Denied.", permissionDecisionLabel(acp.PermissionDeny))
}

func TestExtractTextFromMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want string
	}{
		{"nil message", nil, ""},
		{"text only", &telego.Message{Text: "hello"}, "hello"},
		{"caption only", &telego.Message{Text: "", Caption: "photo caption"}, "photo caption"},
		{"text over caption", &telego.Message{Text: "text", Caption: "caption"}, "text"},
		{"whitespace text uses caption", &telego.Message{Text: "   ", Caption: "caption"}, "caption"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextFromMessage(tt.msg)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Task 2 parity: document handling, filename fallback, command-skip ---

func TestProcessNonImageDocument_UTF8TextFile(t *testing.T) {
	// UTF-8 decodable -> TextContent set (text file semantic for Task 3)
	data := []byte("hello from file")
	fd := processNonImageDocument(data, "text/plain", "note.txt")
	require.NotNil(t, fd.TextContent)
	assert.Equal(t, "hello from file", *fd.TextContent)
	assert.Equal(t, []byte("hello from file"), fd.Data)
	assert.Equal(t, "note.txt", fd.Name)
	assert.Equal(t, "text/plain", fd.MIMEType)
}

func TestProcessNonImageDocument_BinaryFile(t *testing.T) {
	// Non-UTF-8 -> TextContent nil (binary semantic)
	data := []byte{0xff, 0xfe}
	fd := processNonImageDocument(data, "application/octet-stream", "x.bin")
	assert.Nil(t, fd.TextContent)
	assert.Equal(t, []byte{0xff, 0xfe}, fd.Data)
	assert.Equal(t, "x.bin", fd.Name)
}

func TestProcessNonImageDocument_FileNameFallback(t *testing.T) {
	// Empty filename -> "attachment.bin" (Python parity)
	fd := processNonImageDocument([]byte("x"), "text/plain", "")
	assert.Equal(t, "attachment.bin", fd.Name)
}

func TestIsCommandToSkip_CaptionCommand(t *testing.T) {
	// Caption-only command (e.g. photo with caption /help) should be skipped
	msg := &telego.Message{Text: "", Caption: "/help"}
	assert.True(t, isCommandToSkip(msg))
}

func TestIsCommandToSkip_NonLeadingSlash_NotSkipped(t *testing.T) {
	// Non-leading slash (e.g. "hello /help world") does NOT count as command for skip logic
	msg := &telego.Message{Text: "hello /help world"}
	assert.False(t, isCommandToSkip(msg))
}

func TestHandleUserMessage_CommandInCaption_SkipsPrompt(t *testing.T) {
	// Message with only command in caption should not be sent as prompt content.
	// Use short HTTP timeout and synchronous HandleUpdate to avoid flaky sleeps.
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	mock := &mockAgentService{
		activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."},
		promptReply:   &acp.AgentReply{Text: "ok"},
	}
	b := NewBridge(tBot, mock, Config{})
	updates := make(chan telego.Update, 1)
	require.NoError(t, b.RegisterHandlers(updates))

	update := telego.Update{
		UpdateID: 1,
		Message: &telego.Message{
			MessageID: 1,
			Chat:      telego.Chat{ID: 123},
			From:      &telego.User{ID: 100, Username: "user"},
			Caption:   "/help",
			Photo:     []telego.PhotoSize{{FileID: "fake-photo"}},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	called, _ := mock.getPromptState()
	assert.False(t, called, "Prompt should not be called when caption is a command")
}

func TestHandleUserMessage_CaptionExtraction(t *testing.T) {
	// Use short HTTP timeout and synchronous HandleUpdate to avoid flaky sleeps.
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	mock := &mockAgentService{
		activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."},
		promptReply:   &acp.AgentReply{Text: "ok"},
	}
	b := NewBridge(tBot, mock, Config{})
	updates := make(chan telego.Update, 1)
	require.NoError(t, b.RegisterHandlers(updates))

	update := telego.Update{
		UpdateID: 1,
		Message: &telego.Message{
			MessageID: 1,
			Chat:      telego.Chat{ID: 123},
			From:      &telego.User{ID: 100, Username: "user"},
			Caption:   "describe this",
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	_, input := mock.getPromptState()
	require.NotNil(t, input)
	assert.Equal(t, "describe this", input.Text)
}

func TestFormatActivityMessage_Execute(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityExecute,
		Label:  "Execute",
		Detail: "Run git log",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**Execute**")
	assert.Contains(t, got, "git log")
	assert.Contains(t, got, "```")
}

func TestFormatActivityMessage_Read(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityRead,
		Label:  "Read",
		Detail: "Read /workspace/src/main.go",
	}
	got := formatActivityMessage(block, "/workspace")
	assert.Contains(t, got, "**Read**")
	assert.Contains(t, got, "src/main.go")
	assert.NotContains(t, got, "/workspace/")
}

func TestFormatActivityMessage_Failed(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityExecute,
		Label:  "Execute",
		Detail: "Run failing command",
		Status: "failed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "_Failed_") // Python parity: italic
}

func TestFormatActivityMessage_Think(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:  acp.ActivityThink,
		Label: "Thinking...",
	}
	got := formatActivityMessage(block, "")
	assert.Equal(t, "**Thinking...**", got)
}

func TestFormatActivityMessage_WithBlockText(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityExecute,
		Label:  "Execute",
		Detail: "Run ls",
		Text:   "file1.go\nfile2.go",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**Execute**")
	assert.Contains(t, got, "file1.go\nfile2.go")
}

// Python parity: _format_activity_block uses _Failed_ (italic), not *Failed* (bold).
func TestFormatActivityMessage_Failed_UsesItalic(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityExecute,
		Label:  "⚙️ Running",
		Detail: "Run failing command",
		Status: "failed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "_Failed_", "Python uses italic _Failed_, not bold *Failed*")
	assert.NotContains(t, got, "*Failed*")
}

// Python parity: parts are joined with "\n\n" (double newline), not "\n".
func TestFormatActivityMessage_PartsSeparatedByDoubleNewline(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityRead,
		Label:  "📖 Reading",
		Detail: "Read README.md",
		Status: "completed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**📖 Reading**\n\n`README.md`", "Python joins with \\n\\n")
}

// Python parity: _path_prefix_for_kind returns None for write; do NOT strip "Write " prefix.
func TestFormatActivityMessage_Write_DoesNotStripPrefix(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivityWrite,
		Label:  "✍️ Writing",
		Detail: "Write foo.txt",
		Status: "completed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**✍️ Writing**")
	assert.Contains(t, got, "Write foo.txt", "Python does not strip Write prefix; show full title")
	assert.NotContains(t, got, "`foo.txt`")
}

// Python parity: "annual report" contains "repo" as substring but \brepo\b does not match; use neutral label.
func TestFormatActivityMessage_Search_ReportNotMisclassified(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivitySearch,
		Label:  "🔎 Querying",
		Detail: `Query: "annual report"`,
		Text:   "",
		Status: "completed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**🔎 Querying**", "report must not match \\brepo\\b; use neutral label")
	assert.NotContains(t, got, "Querying project")
}

// Python parity: "rg" as whole word matches local; use "🔎 Querying project".
func TestFormatActivityMessage_Search_RgMatchesLocal(t *testing.T) {
	block := acp.ActivityBlock{
		Kind:   acp.ActivitySearch,
		Label:  "🔎 Querying",
		Detail: "Query: rg",
		Text:   "",
		Status: "completed",
	}
	got := formatActivityMessage(block, "")
	assert.Contains(t, got, "**🔎 Querying project**", "rg as word matches local in Python")
}

func TestParseResumeArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantIdx   *int
		wantValid bool
	}{
		{"no args", nil, nil, true},
		{"index only", []string{"2"}, intPtr(2), true},
		{"non-numeric", []string{"workspace-a"}, nil, false},
		{"too many args", []string{"1", "workspace-a"}, nil, false},
		{"blank arg", []string{" "}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, valid := parseResumeArgs(tt.args)
			assert.Equal(t, tt.wantValid, valid, "valid")
			if !valid {
				return
			}
			if tt.wantIdx == nil {
				assert.Nil(t, idx)
			} else {
				require.NotNil(t, idx)
				assert.Equal(t, *tt.wantIdx, *idx)
			}
		})
	}
}

func intPtr(n int) *int { return &n }

// --- Task 4 parity: busy queue lifecycle and pending context ---

func TestBusyQueue_NormalDrain_ClearsMarkupOnly(t *testing.T) {
	// Normal drain path must clear busy inline keyboard markup only (not rewrite to "✅ Sent.").
	mock := newBlockingMock()
	b := NewBridge(nil, mock, Config{})
	chatID := int64(456)
	ctx := context.Background()

	var clearMarkupOnly bool
	b.onClearBusyNotification = func(only bool) { clearMarkupOnly = only }

	lock := b.chatMutex(chatID)
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer lock.Unlock()
		defer close(done)
		b.runPromptLoop(ctx, chatID, acp.PromptInput{Text: "first"})
	}()

	select {
	case <-mock.promptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Prompt did not block")
	}

	b.pendingMu.Lock()
	b.pendingByChat[chatID] = &pendingPrompt{
		input:       acp.PromptInput{Text: "second"},
		chatID:      chatID,
		token:       "t1",
		notifyMsgID: 1,
	}
	b.pendingMu.Unlock()

	mock.Unblock()
	mock.Unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPromptLoop did not finish")
	}

	assert.True(t, clearMarkupOnly, "drain path must clear markup only, not edit text to ✅ Sent.")
}

func TestBusyCallback_CancelFailure_AnswersAndClearsMarkup(t *testing.T) {
	// On cancel failure: answer "Cancel failed.", clear reply markup, pending stays for drain loop.
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	mock := &mockAgentService{
		activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."},
		cancelErr:     errors.New("cancel failed"),
	}
	b := NewBridge(tBot, mock, Config{AllowedUserIDs: []int64{100}})
	token := "tok-123"
	b.pendingMu.Lock()
	b.pendingByChat[123] = &pendingPrompt{
		input:  acp.PromptInput{Text: "queued"},
		chatID: 123,
		token:  token,
	}
	b.pendingMu.Unlock()

	var answer string
	b.onBusyCancelFailure = func(a string) { answer = a }

	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-1",
			Data: "busy|" + token,
			From: telego.User{ID: 100},
			Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: 123},
				Text:      "⏳ Agent is busy.",
			},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	assert.Equal(t, busyCancelFailedText, answer, "must answer Cancel failed. on cancel failure")
	b.pendingMu.Lock()
	p := b.pendingByChat[123]
	b.pendingMu.Unlock()
	require.NotNil(t, p, "pending must stay on cancel failure for drain loop")
	assert.Equal(t, "queued", p.input.Text)
}

func TestBusyCallback_MatchingToken_KeepsPendingForDrain(t *testing.T) {
	// With matching token and cancel success: answer "✅ Sent.", edit text, clear markup;
	// pending must remain for drain loop (Python keeps it).
	blocking := newBlockingMock()
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	b := NewBridge(tBot, blocking, Config{})
	chatID := int64(789)
	ctx := context.Background()
	token := "match-tok"

	b.pendingMu.Lock()
	b.pendingByChat[chatID] = &pendingPrompt{
		input:       acp.PromptInput{Text: "from-callback"},
		chatID:      chatID,
		token:       token,
		notifyMsgID: 1,
	}
	b.pendingMu.Unlock()

	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	lock := b.chatMutex(chatID)
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer lock.Unlock()
		defer close(done)
		b.runPromptLoop(ctx, chatID, acp.PromptInput{Text: "initial"})
	}()

	select {
	case <-blocking.promptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not block")
	}

	callbackDone := make(chan struct{})
	b.onBusyMatchingTokenDone = func() { close(callbackDone) }

	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-2",
			Data: "busy|" + token,
			From: telego.User{ID: 100},
			Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: chatID},
				Text:      "⏳ Agent is busy.",
			},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	// Wait for callback to complete (Cancel unblocks first Prompt)
	select {
	case <-callbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not complete")
	}
	// Second Prompt (for "from-callback") needs Unblock
	blocking.Unblock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPromptLoop did not finish")
	}

	assert.Equal(t, 2, blocking.promptCount, "both initial and from-callback prompts must run")
	_, input := blocking.getPromptState()
	require.NotNil(t, input)
	assert.Equal(t, "from-callback", input.Text)
}

func TestHandleUserMessage_PlainText(t *testing.T) {
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc", telego.WithDiscardLogger())
	require.NoError(t, err)

	mock := &mockAgentService{
		activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."},
		promptReply:   &acp.AgentReply{Text: "ok"},
	}
	b := NewBridge(tBot, mock, Config{})
	updates := make(chan telego.Update, 1)
	require.NoError(t, b.RegisterHandlers(updates))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = b.Run(ctx)
	}()
	time.Sleep(50 * time.Millisecond)

	updates <- telego.Update{
		UpdateID: 1,
		Message: &telego.Message{
			MessageID: 1,
			Chat:      telego.Chat{ID: 123},
			From:      &telego.User{ID: 100, Username: "user"},
			Text:      "hello",
		},
	}

	require.Eventually(t, func() bool {
		called, _ := mock.getPromptState()
		return called
	}, 2*time.Second, 50*time.Millisecond, "Prompt should have been called")

	_, input := mock.getPromptState()
	require.NotNil(t, input)
	assert.Equal(t, "hello", input.Text)
}
