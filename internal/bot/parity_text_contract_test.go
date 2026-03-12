package bot

import (
	"context"
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

// Parity contract tests: user-visible text and callback behavior must match Python exactly.

func TestParity_AccessDeniedText(t *testing.T) {
	// Access denied text must be exactly "Access denied for this bot." in command/message denial flow.
	assert.Equal(t, "Access denied for this bot.", accessDeniedText)
}

func TestParity_StdioLimitExceededText(t *testing.T) {
	// Stdio limit exceeded message must match Python exactly.
	assert.Equal(t, "Agent output exceeded ACP stdio limit. Restart with a higher `--acp-stdio-limit` (or `ACP_STDIO_LIMIT`).", stdioLimitExceededText)
}

func TestParity_PermissionDecisionLabels(t *testing.T) {
	// Permission decision labels must match Python exactly.
	assert.Equal(t, "Approved for this session.", permissionDecisionLabel(acp.PermissionAlways))
	assert.Equal(t, "Approved this time.", permissionDecisionLabel(acp.PermissionThisTime))
	assert.Equal(t, "Denied.", permissionDecisionLabel(acp.PermissionDeny))
}

func TestParity_CallbackDataFormat_Perm(t *testing.T) {
	// Callback payload must use Python style: perm|reqID|action
	reqID := "req-123"
	data := buildPermCallbackData(reqID, "always")
	assert.True(t, strings.HasPrefix(data, "perm|"), "perm callback should use perm| prefix")
	assert.Equal(t, "perm|req-123|always", data)

	dataOnce := buildPermCallbackData(reqID, "once")
	assert.Equal(t, "perm|req-123|once", dataOnce)

	dataDeny := buildPermCallbackData(reqID, "deny")
	assert.Equal(t, "perm|req-123|deny", dataDeny)
}

func TestParity_CallbackDataFormat_Resume(t *testing.T) {
	// Callback payload must use Python style: resume|index (0-based)
	sessions := []acp.SessionInfo{{SessionID: "s1", Workspace: "."}}
	kb := buildResumeKeyboard(sessions)
	require.Len(t, kb.InlineKeyboard, 1)
	assert.Equal(t, "resume|0", kb.InlineKeyboard[0][0].CallbackData)
}

func TestParity_CallbackDataFormat_Busy(t *testing.T) {
	// Callback payload must use Python style: busy|token
	data := buildBusyCallbackData("test-token-abc")
	assert.True(t, strings.HasPrefix(data, "busy|"), "busy callback should use busy| prefix")
	assert.Equal(t, "busy|test-token-abc", data)
}

func TestParity_BusyQueue_ButtonText(t *testing.T) {
	// Queue message button must be "Send now" (no extra emoji), Python parity.
	assert.Equal(t, "Send now", busySendNowButtonText)
}

func TestParity_BusyCallback_SuccessPath_AnswerAndEditText(t *testing.T) {
	// Matching token success: answer and edit must be exactly "✅ Sent." (Python parity).
	assert.Equal(t, "✅ Sent.", busySentText)
}

func TestParity_BusyCallback_StaleToken_AnswerText(t *testing.T) {
	// Stale token: answer must be exactly "Already sent." (Python parity).
	assert.Equal(t, "Already sent.", busyAlreadySentText)
}

func TestParity_BusyCallback_CancelFailure_AnswerText(t *testing.T) {
	// Cancel failure: answer must be exactly "Cancel failed." (Python parity).
	assert.Equal(t, "Cancel failed.", busyCancelFailedText)
}

func TestParity_BusyCallback_AccessDenied_AnswerText(t *testing.T) {
	// Callback access denied: answer must be exactly "Access denied." (Python parity).
	assert.Equal(t, "Access denied.", accessDeniedCallbackText)
}

func TestParity_BusyCallback_AccessDenied_AnswersAndReturns(t *testing.T) {
	// When user not in allowlist: answer "Access denied." and return without processing token.
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	var answer string
	mock := &mockAgentService{}
	b := NewBridge(tBot, mock, Config{AllowedUserIDs: []int64{100}}) // only user 100 allowed
	b.onBusyAccessDenied = func(a string) { answer = a }
	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	// User 99 not in allowlist; callback with valid-looking token
	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-denied-1",
			Data: "busy|any-token",
			From: telego.User{ID: 99, Username: "denied"},
			Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: 123},
				Text:      "⏳ Agent is busy.",
			},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	assert.Equal(t, accessDeniedCallbackText, answer, "must answer Access denied. when user not in allowlist")
}

// Task 5: Permission keyboard must only show buttons for available actions (Python _permission_keyboard parity).
func TestParity_PermissionKeyboard_OnlyAvailableActions(t *testing.T) {
	// always -> "Always", once -> "This time", deny -> "Deny"; callback perm|reqID|action
	kb := buildPermissionKeyboard("req-1", []acp.PermissionDecision{acp.PermissionDeny})
	require.Len(t, kb.InlineKeyboard, 1)
	require.Len(t, kb.InlineKeyboard[0], 1)
	assert.Equal(t, "Deny", kb.InlineKeyboard[0][0].Text)
	assert.Equal(t, "perm|req-1|deny", kb.InlineKeyboard[0][0].CallbackData)

	kb2 := buildPermissionKeyboard("req-2", []acp.PermissionDecision{
		acp.PermissionAlways, acp.PermissionThisTime, acp.PermissionDeny,
	})
	require.Len(t, kb2.InlineKeyboard, 1)
	require.Len(t, kb2.InlineKeyboard[0], 3)
	assert.Equal(t, "Always", kb2.InlineKeyboard[0][0].Text)
	assert.Equal(t, "perm|req-2|always", kb2.InlineKeyboard[0][0].CallbackData)
	assert.Equal(t, "This time", kb2.InlineKeyboard[0][1].Text)
	assert.Equal(t, "perm|req-2|once", kb2.InlineKeyboard[0][1].CallbackData)
	assert.Equal(t, "Deny", kb2.InlineKeyboard[0][2].Text)
	assert.Equal(t, "perm|req-2|deny", kb2.InlineKeyboard[0][2].CallbackData)

	// once + deny only (no always)
	kb3 := buildPermissionKeyboard("req-3", []acp.PermissionDecision{acp.PermissionThisTime, acp.PermissionDeny})
	require.Len(t, kb3.InlineKeyboard[0], 2)
	assert.Equal(t, "This time", kb3.InlineKeyboard[0][0].Text)
	assert.Equal(t, "Deny", kb3.InlineKeyboard[0][1].Text)
}

// Task 5: Permission callback must reject unavailable action, answer "Request expired."
func TestParity_PermissionCallback_RejectsUnavailableAction(t *testing.T) {
	client := &http.Client{
		Timeout:   1 * time.Second,
		Transport: &mockHTTPTransport{statusCode: 200, body: `{"ok":true,"result":true}`},
	}
	tBot, err := telego.NewBot("123456789:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	mock := &mockAgentService{activeSession: &acp.SessionInfo{SessionID: "s1", Workspace: "."}}
	b := NewBridge(tBot, mock, Config{AllowedUserIDs: []int64{100}})
	ch := make(chan acp.PermissionResponse, 1)
	b.pendingPerms["req-once-only"] = ch
	b.pendingPermActions["req-once-only"] = []acp.PermissionDecision{acp.PermissionThisTime, acp.PermissionDeny}

	var answer string
	b.onPermCallbackAnswer = func(a string) { answer = a }

	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	// User clicks "always" but only once+deny were available
	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-1",
			Data: "perm|req-once-only|always",
			From: telego.User{ID: 100},
			Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: 123},
				Text:      "Permission request",
			},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	assert.Equal(t, "Request expired.", answer)
	// Channel must not receive (RespondPermission not called for invalid action)
	select {
	case <-ch:
		t.Fatal("RespondPermission must not be called for unavailable action")
	default:
	}
}

// Task 5: Permission decision edit format must be \nDecision: <label> (Python parity, no emoji).
func TestParity_PermissionDecision_EditFormat(t *testing.T) {
	// Format must match Python: single newline + "Decision: " + label, no emoji
	assert.Equal(t, "\nDecision: Denied.", formatPermissionDecisionEdit("Denied."))
	assert.Equal(t, "\nDecision: Approved this time.", formatPermissionDecisionEdit("Approved this time."))
}

// Task 8: No active session when prompt returns None/ErrNoActiveSession (Python _request_reply parity).
func TestParity_NoActiveSessionPromptText(t *testing.T) {
	assert.Equal(t, "No active session. Send a message again or use /new [workspace].", noActiveSessionPromptText)
}

// Task 8: No resumable sessions message (Python parity).
func TestParity_NoResumableSessionsText(t *testing.T) {
	assert.Equal(t, "No resumable sessions found.", noResumableSessionsText)
}

// Task 8: Resume callback when candidates nil - answer "Selection expired." (Python parity).
func TestParity_ResumeCallback_SelectionExpiredText(t *testing.T) {
	assert.Equal(t, "Selection expired.", selectionExpiredText)
}

// Task 8: Resume callback when index invalid - answer "Invalid selection." (Python parity).
func TestParity_ResumeCallback_InvalidSelectionText(t *testing.T) {
	assert.Equal(t, "Invalid selection.", invalidSelectionText)
}

// Task 8: Resume callback with non-numeric index answers "Invalid selection." (Python parity).
func TestParity_ResumeCallback_NonNumericIndex(t *testing.T) {
	client := &http.Client{
		Timeout:   1 * time.Second,
		Transport: &mockHTTPTransport{statusCode: 200, body: `{"ok":true,"result":true}`},
	}
	tBot, err := telego.NewBot("123456789:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	mock := &mockAgentService{}
	b := NewBridge(tBot, mock, Config{AllowedUserIDs: []int64{100}})

	var answer string
	b.onResumeCallbackAnswer = func(a string) { answer = a }

	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-nonnumeric-1",
			Data: "resume|abc",
			From: telego.User{ID: 100, Username: "allowed"},
			Message: &telego.Message{
				MessageID: 1,
				Chat:      telego.Chat{ID: 123},
				Text:      "Pick a session to resume:",
			},
		},
	}
	err = b.handler.BaseGroup().HandleUpdate(context.Background(), tBot, update)
	require.NoError(t, err)

	assert.Equal(t, invalidSelectionText, answer, "non-numeric index must answer Invalid selection.")
}

func TestParity_BusyCallback_StaleToken_AnswersAndClearsMarkup(t *testing.T) {
	// When callback has stale/mismatched token: answer "Already sent." and clear inline keyboard markup.
	// Use short timeout to avoid real network calls; test asserts onBusyStale contract only.
	client := &http.Client{Timeout: 1 * time.Millisecond}
	tBot, err := telego.NewBot("1234567890:aaaabbbbaaaabbbbaaaabbbbaaaabbbbccc",
		telego.WithDiscardLogger(),
		telego.WithHTTPClient(client))
	require.NoError(t, err)

	var recorded struct {
		mu          sync.Mutex
		answer      string
		clearMarkup bool
	}
	mock := &mockAgentService{}
	b := NewBridge(tBot, mock, Config{AllowedUserIDs: []int64{}}) // allow all
	b.onBusyStale = func(answer string, clearMarkup bool) {
		recorded.mu.Lock()
		recorded.answer = answer
		recorded.clearMarkup = clearMarkup
		recorded.mu.Unlock()
	}
	updates := make(chan telego.Update, 4)
	require.NoError(t, b.RegisterHandlers(updates))

	// Invoke handler directly instead of via channel (avoids Start/updates flow)
	update := telego.Update{
		UpdateID: 1,
		CallbackQuery: &telego.CallbackQuery{
			ID:   "cb-stale-1",
			Data: "busy|stale-token-xyz",
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

	recorded.mu.Lock()
	answer := recorded.answer
	clearMarkup := recorded.clearMarkup
	recorded.mu.Unlock()

	assert.Equal(t, busyAlreadySentText, answer)
	assert.True(t, clearMarkup, "stale busy callback must clear inline keyboard markup")
}
