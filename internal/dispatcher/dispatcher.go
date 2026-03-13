package dispatcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhu327/acpclaw/internal/domain"
)

// MemoryService is the interface dispatcher needs from the memory subsystem.
// By depending on an interface rather than *memory.Service, dispatcher is decoupled
// from the concrete memory infrastructure.
type MemoryService interface {
	AppendHistory(chatID, role, text string) error
	SummarizeSession(ctx context.Context, chatID string, summarizer domain.Summarizer) error
}

// Config holds Dispatcher configuration.
type Config struct {
	DefaultWorkspace string
	AllowedUserIDs   []int64
	AllowedUsernames []string
	AutoSummarize    bool
	NewSummarizer    func(chatID string) domain.Summarizer // injected by cmd/
}

type pendingPrompt struct {
	input        domain.PromptInput
	chatID       string
	token        string
	notifyMsgID  int
	replyToMsgID int
}

type pendingPermission struct {
	ch     chan domain.PermissionResponse
	chatID string
}

const permissionRequestTTL = 5 * time.Minute

// Dispatcher routes inbound messages to the AgentService.
type Dispatcher struct {
	agentSvc domain.AgentService
	cfg      Config
	ctx      context.Context
	cancel   context.CancelFunc

	convMu             sync.Map // chatID (string) -> *sync.Mutex
	implicitStartLocks sync.Map // chatID (string) -> *sync.Mutex
	cancelRequested    sync.Map // chatID (string) -> struct{}

	pendingByChat map[string]*pendingPrompt
	pendingMu     sync.Mutex

	pendingPerms  sync.Map // reqID -> *pendingPermission
	permActions   map[string][]domain.PermissionDecision
	permActionsMu sync.Mutex

	pendingResumeChoices map[string][]domain.SessionInfo
	resumeChoicesMu      sync.Mutex

	activeResponders sync.Map // chatID (string) -> domain.Responder

	memorySvc MemoryService
}

// New creates a new Dispatcher.
func New(cfg Config) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		cfg:                  cfg,
		ctx:                  ctx,
		cancel:               cancel,
		permActions:          make(map[string][]domain.PermissionDecision),
		pendingByChat:        make(map[string]*pendingPrompt),
		pendingResumeChoices: make(map[string][]domain.SessionInfo),
	}
}

// Shutdown cancels the dispatcher context.
func (d *Dispatcher) Shutdown() {
	d.cancel()
}

// SetMemoryService sets the MemoryService for the Dispatcher.
func (d *Dispatcher) SetMemoryService(svc MemoryService) {
	d.memorySvc = svc
}

// SetAgentService sets the AgentService for the Dispatcher.
func (d *Dispatcher) SetAgentService(svc domain.AgentService) {
	d.agentSvc = svc
	if svc != nil {
		d.setupCallbacks()
	}
}

func (d *Dispatcher) getOrCreateMutex(chatID string) *sync.Mutex {
	v, _ := d.convMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (d *Dispatcher) implicitStartMutex(chatID string) *sync.Mutex {
	v, _ := d.implicitStartLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func randomToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Handle is the MessageHandler registered on each Channel.
func (d *Dispatcher) Handle(msg domain.InboundMessage, resp domain.Responder) {
	if cmd := parseCommand(msg.Text); cmd != "" {
		d.execCommand(cmd, msg, resp)
		return
	}

	chatID := msg.ChatID
	if chatID == "" {
		slog.Error("invalid chatID", "chat_id", msg.ChatID)
		return
	}

	if d.agentSvc == nil {
		resp.Reply(domain.OutboundMessage{Text: "Agent not configured."}) //nolint:errcheck
		return
	}

	// Implicit session start (separate lock to avoid blocking queue check)
	if d.agentSvc.ActiveSession(chatID) == nil {
		startLock := d.implicitStartMutex(chatID)
		startLock.Lock()
		if d.agentSvc.ActiveSession(chatID) == nil {
			workspace := d.cfg.DefaultWorkspace
			if workspace == "" {
				workspace = "."
			}
			if err := d.agentSvc.NewSession(d.ctx, chatID, workspace); err != nil {
				startLock.Unlock()
				resp.Reply(domain.OutboundMessage{Text: "❌ Failed to start session."}) //nolint:errcheck
				return
			}
		}
		startLock.Unlock()
	}

	input := convertToPromptInput(msg)

	// TryLock: if agent is busy, queue the message
	lock := d.getOrCreateMutex(msg.ChatID)
	if !lock.TryLock() {
		replyToMsgID, _ := strconv.Atoi(msg.ID)
		d.queueBusyPrompt(chatID, input, resp, replyToMsgID)
		return
	}
	defer lock.Unlock()

	d.activeResponders.Store(chatID, resp)
	defer d.activeResponders.Delete(chatID)

	d.runPromptLoop(d.ctx, chatID, input, resp)
}

func (d *Dispatcher) queueBusyPrompt(chatID string, input domain.PromptInput, resp domain.Responder, replyToMsgID int) {
	token := randomToken()

	d.pendingMu.Lock()
	old := d.pendingByChat[chatID]
	d.pendingByChat[chatID] = &pendingPrompt{
		input:        input,
		chatID:       chatID,
		token:        token,
		replyToMsgID: replyToMsgID,
	}
	d.pendingMu.Unlock()

	if old != nil && old.notifyMsgID != 0 {
		resp.ClearBusyNotification(old.notifyMsgID) //nolint:errcheck
	}
	if notifyID, err := resp.ShowBusyNotification(token, replyToMsgID); err == nil {
		d.pendingMu.Lock()
		if p := d.pendingByChat[chatID]; p != nil && p.token == token {
			p.notifyMsgID = notifyID
		}
		d.pendingMu.Unlock()
	}
}

func (d *Dispatcher) popPending(chatID string) *pendingPrompt {
	d.pendingMu.Lock()
	p := d.pendingByChat[chatID]
	delete(d.pendingByChat, chatID)
	d.pendingMu.Unlock()
	return p
}

func (d *Dispatcher) runPromptLoop(
	ctx context.Context,
	chatID string,
	input domain.PromptInput,
	resp domain.Responder,
) {
	for {
		d.appendUserToHistory(chatID, input.Text)

		reply, err := d.agentSvc.Prompt(ctx, chatID, input)
		d.handlePromptResult(chatID, resp, reply, err)

		p := d.popPending(chatID)
		if p == nil {
			return
		}
		resp.ClearBusyNotification(p.notifyMsgID) //nolint:errcheck
		input = p.input
	}
}

func (d *Dispatcher) appendUserToHistory(chatID string, text string) {
	if d.memorySvc == nil || text == "" {
		return
	}
	if err := d.memorySvc.AppendHistory(chatID, "user", text); err != nil {
		slog.Warn("failed to append user message to history", "chat_id", chatID, "error", err)
	}
}

func (d *Dispatcher) handlePromptResult(chatID string, resp domain.Responder, reply *domain.AgentReply, err error) {
	if err != nil {
		if _, wasCancelled := d.cancelRequested.LoadAndDelete(chatID); !wasCancelled {
			d.sendPromptError(chatID, resp, err)
		}
	}
	if d.hasReplyContent(reply) {
		if err == nil && d.memorySvc != nil && reply.Text != "" {
			if appendErr := d.memorySvc.AppendHistory(chatID, "assistant", reply.Text); appendErr != nil {
				slog.Warn("failed to append assistant reply to history", "chat_id", chatID, "error", appendErr)
			}
		}
		d.sendReply(resp, reply)
	}
}

func (d *Dispatcher) hasReplyContent(reply *domain.AgentReply) bool {
	return reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0)
}

func (d *Dispatcher) sendPromptError(chatID string, resp domain.Responder, err error) {
	switch {
	case errors.Is(err, domain.ErrAgentOutputLimitExceeded):
		resp.Reply(domain.OutboundMessage{ //nolint:errcheck
			Text: "Agent output exceeded ACP stdio limit. Restart with a higher `--acp-stdio-limit` (or `ACP_STDIO_LIMIT`).",
		})
	case errors.Is(err, domain.ErrNoActiveSession):
		resp.Reply(domain.OutboundMessage{ //nolint:errcheck
			Text: "No active session. Send a message again or use /new [workspace].",
		})
	default:
		slog.Error("prompt failed", "chat_id", chatID, "error", err)
		resp.Reply(domain.OutboundMessage{Text: "❌ Failed to process your request."}) //nolint:errcheck
	}
}

func (d *Dispatcher) sendReply(resp domain.Responder, reply *domain.AgentReply) {
	resp.Reply(domain.OutboundMessage{ //nolint:errcheck
		Text:   reply.Text,
		Images: reply.Images,
		Files:  reply.Files,
	})
}

// convertToPromptInput converts a channel InboundMessage to a domain PromptInput.
func convertToPromptInput(msg domain.InboundMessage) domain.PromptInput {
	input := domain.PromptInput{Text: msg.Text}
	for _, att := range msg.Attachments {
		switch att.MediaType {
		case "image":
			input.Images = append(input.Images, domain.ImageData{
				MIMEType: "image/jpeg",
				Data:     att.Data,
				Name:     att.FileName,
			})
		default:
			fd := domain.FileData{
				MIMEType: att.MediaType,
				Data:     att.Data,
				Name:     att.FileName,
			}
			if utf8.Valid(att.Data) {
				s := string(att.Data)
				fd.TextContent = &s
			}
			if fd.MIMEType == "" {
				fd.MIMEType = "application/octet-stream"
			}
			if fd.Name == "" {
				fd.Name = "attachment.bin"
			}
			input.Files = append(input.Files, fd)
		}
	}
	return input
}

func (d *Dispatcher) setupCallbacks() {
	d.agentSvc.SetPermissionHandler(func(chatID string, req domain.PermissionRequest) <-chan domain.PermissionResponse {
		ch := make(chan domain.PermissionResponse, 1)
		pp := &pendingPermission{ch: ch, chatID: chatID}
		d.pendingPerms.Store(req.ID, pp)

		d.permActionsMu.Lock()
		d.permActions[req.ID] = req.AvailableActions
		d.permActionsMu.Unlock()

		go d.expirePermissionRequest(req.ID, ch)

		if v, ok := d.activeResponders.Load(chatID); ok {
			resp := v.(domain.Responder)
			resp.ShowPermissionUI(domain.ChannelPermissionRequest{ //nolint:errcheck
				ID:               req.ID,
				Tool:             req.Tool,
				Description:      req.Description,
				AvailableActions: permDecisionsToStrings(req.AvailableActions),
			})
		}
		return ch
	})

	d.agentSvc.SetActivityHandler(func(chatID string, block domain.ActivityBlock) {
		if v, ok := d.activeResponders.Load(chatID); ok {
			resp := v.(domain.Responder)
			workspace := ""
			if info := d.agentSvc.ActiveSession(chatID); info != nil {
				workspace = info.Workspace
			}
			b := block
			b.Workspace = workspace
			resp.SendActivity(b) //nolint:errcheck
		}
	})
}

func (d *Dispatcher) expirePermissionRequest(reqID string, ch chan domain.PermissionResponse) {
	timer := time.NewTimer(permissionRequestTTL)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-d.ctx.Done():
		return
	}

	if v, ok := d.pendingPerms.LoadAndDelete(reqID); ok {
		pp := v.(*pendingPermission)
		if pp.ch == ch {
			select {
			case ch <- domain.PermissionResponse{Decision: domain.PermissionDeny}:
			default:
			}
		}
	}
	d.permActionsMu.Lock()
	delete(d.permActions, reqID)
	d.permActionsMu.Unlock()
}

func permDecisionsToStrings(decisions []domain.PermissionDecision) []string {
	out := make([]string, len(decisions))
	for i, dec := range decisions {
		switch dec {
		case domain.PermissionAlways:
			out[i] = "always_allow"
		case domain.PermissionThisTime:
			out[i] = "allow_once"
		case domain.PermissionDeny:
			out[i] = "deny"
		default:
			out[i] = string(dec)
		}
	}
	return out
}

// RespondPermission resolves a pending permission request.
func (d *Dispatcher) RespondPermission(reqID string, decision domain.PermissionDecision) {
	if v, ok := d.pendingPerms.LoadAndDelete(reqID); ok {
		pp := v.(*pendingPermission)
		select {
		case pp.ch <- domain.PermissionResponse{Decision: decision}:
		default:
		}
		if decision == domain.PermissionAlways && d.agentSvc != nil {
			d.agentSvc.SetSessionPermissionMode(pp.chatID, domain.PermissionModeApprove)
		}
	}
	d.permActionsMu.Lock()
	delete(d.permActions, reqID)
	d.permActionsMu.Unlock()
}

// PermissionActions returns available actions for a pending permission request.
func (d *Dispatcher) PermissionActions(reqID string) []domain.PermissionDecision {
	d.permActionsMu.Lock()
	defer d.permActionsMu.Unlock()
	return d.permActions[reqID]
}

// HandleBusySendNow is called by Channel when "Send now" button is clicked.
func (d *Dispatcher) HandleBusySendNow(chatID int64, token string) (ok bool, err error) {
	chatIDStr := strconv.FormatInt(chatID, 10)
	d.pendingMu.Lock()
	p := d.pendingByChat[chatIDStr]
	if p == nil || p.token != token {
		d.pendingMu.Unlock()
		return false, nil
	}
	d.pendingMu.Unlock()

	d.cancelRequested.Store(chatIDStr, struct{}{})
	if err := d.agentSvc.Cancel(d.ctx, chatIDStr); err != nil {
		d.cancelRequested.Delete(chatIDStr)
		return false, err
	}
	return true, nil
}

// ResolveResumeChoice resolves a pending resume keyboard selection.
func (d *Dispatcher) ResolveResumeChoice(ctx context.Context, chatID int64, index int) (*domain.SessionInfo, error) {
	chatIDStr := strconv.FormatInt(chatID, 10)
	d.resumeChoicesMu.Lock()
	candidates := d.pendingResumeChoices[chatIDStr]
	delete(d.pendingResumeChoices, chatIDStr)
	d.resumeChoicesMu.Unlock()

	if candidates == nil {
		return nil, fmt.Errorf("selection expired")
	}
	if index < 0 || index >= len(candidates) {
		return nil, fmt.Errorf("invalid selection")
	}
	s := candidates[index]
	if err := d.agentSvc.LoadSession(ctx, chatIDStr, s.SessionID, s.Workspace); err != nil {
		return nil, err
	}
	return &s, nil
}

// IsAllowed checks if a user is in the allowlist.
func (d *Dispatcher) IsAllowed(userID int64, username string) bool {
	return IsAllowed(AllowlistConfig{
		AllowedUserIDs:   d.cfg.AllowedUserIDs,
		AllowedUsernames: d.cfg.AllowedUsernames,
	}, userID, username)
}
