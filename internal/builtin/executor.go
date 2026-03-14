package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
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
	agentSvc       domain.AgentService
	convMu         sync.Map // chatID -> *sync.Mutex
	pendingByChat  map[string]*pendingPrompt
	pendingMu      sync.Mutex
	cancelRequested sync.Map // chatID -> struct{}
	fw             interface {
		ProcessInbound(ctx context.Context, msg domain.InboundMessage, resp domain.Responder) error
	}
	firstPromptPrefix func(chatID string) string
}

func newPromptExecutor(agentSvc domain.AgentService, fw interface {
	ProcessInbound(ctx context.Context, msg domain.InboundMessage, resp domain.Responder) error
}, firstPromptPrefix func(chatID string) string) *promptExecutor {
	return &promptExecutor{
		agentSvc:         agentSvc,
		pendingByChat:    make(map[string]*pendingPrompt),
		fw:               fw,
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
	e.pendingMu.Unlock()

	e.cancelRequested.Store(chatID, struct{}{})
	if err := e.agentSvc.Cancel(context.Background(), chatID); err != nil {
		e.cancelRequested.Delete(chatID)
		return false, err
	}
	return true, nil
}

func (e *promptExecutor) runPromptLoop(ctx context.Context, chatID string, input domain.PromptInput, resp domain.Responder, chat domain.ChatRef, isFirstTurn bool) *domain.Result {
	for {
		if isFirstTurn && e.firstPromptPrefix != nil {
			if prefix := e.firstPromptPrefix(chatID); prefix != "" {
				input.Text = prefix + "\n\n---\n\n" + input.Text
			}
			isFirstTurn = false
		}

		reply, err := e.agentSvc.Prompt(ctx, chatID, input)
		if err != nil {
			if _, wasCancelled := e.cancelRequested.LoadAndDelete(chatID); !wasCancelled {
				return &domain.Result{Text: "❌ Failed to process your request."}
			}
		}
		if reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0) {
			e.sendReply(resp, reply)
		}

		p := e.popPending(chatID)
		if p == nil {
			return nil
		}
		_ = resp.ClearBusyNotification(p.notifyMsgID)
		input = p.input
	}
}

func (e *promptExecutor) sendReply(resp domain.Responder, reply *domain.AgentReply) {
	_ = resp.Reply(domain.OutboundMessage{
		Text:   reply.Text,
		Images: reply.Images,
		Files:  reply.Files,
	})
}

// executePrompt runs the prompt with busy queue logic.
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

	isFirstTurn := e.agentSvc.ActiveSession(chatID) == nil
	return e.runPromptLoop(ctx, chatID, action.Input, tc.Responder, tc.Chat, isFirstTurn)
}

func parseCompositeChatID(chatID string) (channel, rawID string) {
	if ch, raw, ok := strings.Cut(chatID, ":"); ok {
		return ch, raw
	}
	return "", chatID
}

func buildSessionInfoBlock(chatID string, channelName string) string {
	channel, rawChatID := parseCompositeChatID(chatID)
	if channel == "" {
		channel = channelName
		if channel == "" {
			channel = "telegram"
		}
	}
	return "[Session Info]\nchannel: " + channel + "\nchat_id: " + rawChatID + "\n[/Session Info]"
}
