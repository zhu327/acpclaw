package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync"

	"github.com/zhu327/acpclaw/internal/domain"
)

type pendingPrompt struct {
	input        domain.PromptInput
	token        string
	notifyMsgID  int
	replyToMsgID int
}

// promptExecutor handles the busy queue and prompt loop for ActionExecutor.
type promptExecutor struct {
	sessionMgr        domain.SessionManager
	prompter          domain.Prompter
	chatLocks         sync.Map // chatID -> *sync.Mutex
	pendingByChat     map[string]*pendingPrompt
	pendingMu         sync.Mutex
	cancelRequested   sync.Map // chatID -> struct{}
	firstPromptPrefix func(chat domain.ChatRef) string
	prefixMu          sync.Mutex
	lastPrefixSession map[string]string // chatID -> sessionID where prefix was last sent
}

func newPromptExecutor(
	sessionMgr domain.SessionManager,
	prompter domain.Prompter,
	firstPromptPrefix func(chat domain.ChatRef) string,
) *promptExecutor {
	return &promptExecutor{
		sessionMgr:        sessionMgr,
		prompter:          prompter,
		pendingByChat:     make(map[string]*pendingPrompt),
		firstPromptPrefix: firstPromptPrefix,
		lastPrefixSession: make(map[string]string),
	}
}

func (e *promptExecutor) chatLock(chatID string) *sync.Mutex {
	v, _ := e.chatLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func randomToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// parseReplyToMsgID converts a message ID string to int. Returns 0 (no reply-to)
// for empty or unparseable values, which is a safe default for Telegram API.
func parseReplyToMsgID(msgID string) int {
	if msgID == "" {
		return 0
	}
	n, _ := strconv.Atoi(msgID)
	return n
}

func (e *promptExecutor) queueBusyPrompt(
	chatID string,
	input domain.PromptInput,
	resp domain.Responder,
	replyToMsgID int,
) {
	token := randomToken()
	e.pendingMu.Lock()
	old := e.pendingByChat[chatID]
	e.pendingByChat[chatID] = &pendingPrompt{
		input:        input,
		token:        token,
		replyToMsgID: replyToMsgID,
	}
	e.pendingMu.Unlock()

	if old != nil && old.notifyMsgID != 0 {
		_ = resp.ClearBusyNotification(old.notifyMsgID)
	}
	if notifyID, err := resp.ShowBusyNotification(token, replyToMsgID); err == nil {
		e.setPendingNotify(chatID, token, notifyID)
	}
}

func (e *promptExecutor) setPendingNotify(chatID, token string, notifyID int) {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if p := e.pendingByChat[chatID]; p != nil && p.token == token {
		p.notifyMsgID = notifyID
	}
}

func (e *promptExecutor) popPending(chatID string) *pendingPrompt {
	e.pendingMu.Lock()
	p := e.pendingByChat[chatID]
	delete(e.pendingByChat, chatID)
	e.pendingMu.Unlock()
	return p
}

// HandleBusySendNow implements domain.BusyHandler.
func (e *promptExecutor) HandleBusySendNow(chat domain.ChatRef, token string) (bool, error) {
	chatID := chat.CompositeKey()
	e.pendingMu.Lock()
	pending := e.pendingByChat[chatID]
	valid := pending != nil && pending.token == token
	if valid {
		e.cancelRequested.Store(chatID, struct{}{})
	}
	e.pendingMu.Unlock()

	if !valid {
		return false, nil
	}
	if err := e.prompter.Cancel(context.Background(), chat); err != nil {
		e.cancelRequested.Delete(chatID)
		return false, err
	}
	return true, nil
}

func hasReplyContent(reply *domain.AgentReply) bool {
	return reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0)
}

func (e *promptExecutor) applyFirstTurnPrefix(
	input domain.PromptInput,
	chat domain.ChatRef,
	isFirstTurn bool,
) (domain.PromptInput, bool) {
	if !isFirstTurn || e.firstPromptPrefix == nil {
		return input, false
	}
	if prefix := e.firstPromptPrefix(chat); prefix != "" {
		input.Text = prefix + "\n\n---\n\n" + input.Text
	}
	return input, false
}

func (e *promptExecutor) takeNextPending(chatID string, resp domain.Responder) *domain.PromptInput {
	next := e.popPending(chatID)
	if next == nil {
		return nil
	}
	_ = resp.ClearBusyNotification(next.notifyMsgID)
	return &next.input
}

func (e *promptExecutor) runPromptLoop(
	ctx context.Context,
	chatID string,
	input domain.PromptInput,
	resp domain.Responder,
	chat domain.ChatRef,
	isFirstTurn bool,
	state domain.State,
) *domain.Result {
	for {
		input, isFirstTurn = e.applyFirstTurnPrefix(input, chat, isFirstTurn)
		if state != nil {
			state["user_text"] = input.Text
		}

		reply, err := e.prompter.Prompt(ctx, chat, input)
		if err != nil {
			if _, wasCancelled := e.cancelRequested.LoadAndDelete(chatID); !wasCancelled {
				slog.Error("prompt failed", "chat", chatID, "error", err)
				return &domain.Result{Text: "❌ Failed to process your request."}
			}
		} else if state != nil && reply != nil {
			state["reply"] = reply
		}

		if hasReplyContent(reply) {
			return &domain.Result{Reply: reply}
		}

		nextInput := e.takeNextPending(chatID, resp)
		if nextInput == nil {
			return nil
		}
		input = *nextInput
		if state != nil {
			state["reply"] = nil
		}
	}
}

const maxPrefixSessionEntries = 1000

func (e *promptExecutor) checkAndUpdateFirstTurn(chatID, sessionID string) bool {
	e.prefixMu.Lock()
	defer e.prefixMu.Unlock()
	lastSessionID, hadPrefix := e.lastPrefixSession[chatID]
	isFirstTurn := !hadPrefix || lastSessionID != sessionID
	if isFirstTurn {
		if len(e.lastPrefixSession) >= maxPrefixSessionEntries {
			clear(e.lastPrefixSession)
		}
		e.lastPrefixSession[chatID] = sessionID
	}
	return isFirstTurn
}

// executePrompt runs the prompt with busy queue logic.
// Sets state["user_text"] and state["reply"] for StateSaver hooks.
func (e *promptExecutor) executePrompt(
	ctx context.Context,
	action domain.Action,
	tc *domain.TurnContext,
) *domain.Result {
	chatID := tc.Chat.CompositeKey()
	lock := e.chatLock(chatID)
	if !lock.TryLock() {
		e.queueBusyPrompt(chatID, action.Input, tc.Responder, parseReplyToMsgID(tc.Message.ID))
		return &domain.Result{SuppressOutbound: true}
	}
	defer lock.Unlock()

	isFirstTurn := e.checkAndUpdateFirstTurn(chatID, tc.SessionID)
	return e.runPromptLoop(ctx, chatID, action.Input, tc.Responder, tc.Chat, isFirstTurn, tc.State)
}

func buildSessionInfoBlock(chat domain.ChatRef) string {
	return "[Session Info]\nchannel: " + chat.ChannelKind + "\nchat_id: " + chat.ChatID + "\n[/Session Info]"
}
