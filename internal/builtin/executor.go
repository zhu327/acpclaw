package builtin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

// prompterCancelTimeout bounds prompter.Cancel RPC when triggered from timeout or busy callback.
const prompterCancelTimeout = 45 * time.Second

// promptExecutor runs prompt jobs (prefix handling, single Prompt round per job).
type promptExecutor struct {
	sessionMgr        domain.SessionManager
	prompter          domain.Prompter
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
		firstPromptPrefix: firstPromptPrefix,
		lastPrefixSession: make(map[string]string),
	}
}

func hasReplyContent(reply *domain.AgentReply) bool {
	return reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0)
}

func promptCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
}

func promptTimedOut(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func (e *promptExecutor) applyFirstTurnPrefix(
	input domain.PromptInput,
	chat domain.ChatRef,
	isFirstTurn bool,
) domain.PromptInput {
	if !isFirstTurn || e.firstPromptPrefix == nil {
		return input
	}
	if prefix := e.firstPromptPrefix(chat); prefix != "" {
		input.Text = prefix + "\n\n---\n\n" + input.Text
	}
	return input
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

// runPromptJob executes one queued prompt: Prompt + result for the framework pipeline.
// The caller (plugin) performs RenderAndDispatch and SaveState.
func (e *promptExecutor) runPromptJob(ctx context.Context, job *promptJob) *domain.Result {
	chatID := job.tc.Chat.CompositeKey()
	isFirstTurn := e.checkAndUpdateFirstTurn(chatID, job.tc.SessionID)
	input := e.applyFirstTurnPrefix(job.action.Input, job.tc.Chat, isFirstTurn)

	if job.tc.State != nil {
		job.tc.State["user_text"] = input.Text
	}

	reply, err := e.prompter.Prompt(ctx, job.tc.Chat, input, job.tc.Responder)
	if err != nil {
		if promptCancelled(ctx, err) {
			return &domain.Result{Text: "Request cancelled."}
		}
		if promptTimedOut(ctx, err) {
			cancelCtx, cancel := context.WithTimeout(context.Background(), prompterCancelTimeout)
			defer cancel()
			if cancelErr := e.prompter.Cancel(cancelCtx, job.tc.Chat); cancelErr != nil {
				slog.Debug("prompter cancel after timeout", "chat", chatID, "error", cancelErr)
			}
			return &domain.Result{Text: "⏱ Request timed out."}
		}
		slog.Error("prompt failed", "chat", chatID, "error", err)
		return &domain.Result{Text: "❌ Failed to process your request."}
	}
	if job.tc.State != nil && reply != nil {
		job.tc.State["reply"] = reply
	}

	if !hasReplyContent(reply) {
		return nil
	}
	return &domain.Result{Reply: reply}
}

func buildSessionInfoBlock(chat domain.ChatRef) string {
	return "<session_info>\n" +
		"channel: " + chat.ChannelKind + "\n" +
		"chat_id: " + chat.ChatID + "\n" +
		"</session_info>"
}
