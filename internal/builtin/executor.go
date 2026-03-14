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
	chatID       string
	token        string
	notifyMsgID  int
	replyToMsgID int
}

// promptExecutor handles the busy queue and prompt loop for ActionExecutor.
type promptExecutor struct {
	sessionMgr        domain.SessionManager
	prompter          domain.Prompter
	convMu            sync.Map // chatID -> *sync.Mutex
	pendingByChat     map[string]*pendingPrompt
	pendingMu         sync.Mutex
	cancelRequested   sync.Map // chatID -> struct{}
	firstPromptPrefix func(chat domain.ChatRef) string
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
	}
}

func (e *promptExecutor) getOrCreateMutex(chatID string) *sync.Mutex {
	v, _ := e.convMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func randomToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (e *promptExecutor) queueBusyPrompt(chatID string, input domain.PromptInput, resp domain.Responder, replyToMsgID int) {
	token := randomToken()
	e.pendingMu.Lock()
	old := e.pendingByChat[chatID]
	e.pendingByChat[chatID] = &pendingPrompt{
		input:        input,
		chatID:       chatID,
		token:        token,
		replyToMsgID: replyToMsgID,
	}
	e.pendingMu.Unlock()

	if old != nil && old.notifyMsgID != 0 {
		_ = resp.ClearBusyNotification(old.notifyMsgID)
	}
	if notifyID, err := resp.ShowBusyNotification(token, replyToMsgID); err == nil {
		e.pendingMu.Lock()
		if p := e.pendingByChat[chatID]; p != nil && p.token == token {
			p.notifyMsgID = notifyID
		}
		e.pendingMu.Unlock()
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
	p := e.pendingByChat[chatID]
	if p == nil || p.token != token {
		e.pendingMu.Unlock()
		return false, nil
	}
	e.cancelRequested.Store(chatID, struct{}{})
	e.pendingMu.Unlock()

	if err := e.prompter.Cancel(context.Background(), chat); err != nil {
		e.cancelRequested.Delete(chatID)
		return false, err
	}
	return true, nil
}

func (e *promptExecutor) runPromptLoop(ctx context.Context, chatID string, input domain.PromptInput, resp domain.Responder, chat domain.ChatRef, isFirstTurn bool, state domain.State) *domain.Result {
	for {
		if isFirstTurn && e.firstPromptPrefix != nil {
			if prefix := e.firstPromptPrefix(chat); prefix != "" {
				input.Text = prefix + "\n\n---\n\n" + input.Text
			}
			isFirstTurn = false
		}
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

		if reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0) {
			return &domain.Result{Reply: reply}
		}

		p := e.popPending(chatID)
		if p == nil {
			return nil
		}
		_ = resp.ClearBusyNotification(p.notifyMsgID)
		input = p.input
		if state != nil {
			state["reply"] = nil
		}
	}
}

// executePrompt runs the prompt with busy queue logic.
// Sets state["user_text"] and state["reply"] for StateSaver hooks.
func (e *promptExecutor) executePrompt(ctx context.Context, action domain.Action, tc *domain.TurnContext) *domain.Result {
	chatID := tc.Chat.CompositeKey()
	lock := e.getOrCreateMutex(chatID)
	if !lock.TryLock() {
		replyToMsgID := 0
		if tc.Message.ID != "" {
			replyToMsgID, _ = strconv.Atoi(tc.Message.ID)
		}
		e.queueBusyPrompt(chatID, action.Input, tc.Responder, replyToMsgID)
		return &domain.Result{SuppressOutbound: true}
	}
	defer lock.Unlock()

	isFirstTurn := e.sessionMgr.ActiveSession(tc.Chat) == nil
	return e.runPromptLoop(ctx, chatID, action.Input, tc.Responder, tc.Chat, isFirstTurn, tc.State)
}

func buildSessionInfoBlock(chat domain.ChatRef) string {
	return "[Session Info]\nchannel: " + chat.ChannelKind + "\nchat_id: " + chat.ChatID + "\n[/Session Info]"
}
