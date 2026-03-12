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

	"github.com/zhu327/acpclaw/internal/acp"
	"github.com/zhu327/acpclaw/internal/channel"
	"github.com/zhu327/acpclaw/internal/memory"
)

// Config holds Dispatcher configuration.
type Config struct {
	DefaultWorkspace string
	AllowedUserIDs   []int64
	AllowedUsernames []string
	AutoSummarize    bool
}

type pendingPrompt struct {
	input        acp.PromptInput
	chatID       int64
	token        string
	notifyMsgID  int
	replyToMsgID int
}

type pendingPermission struct {
	ch     chan acp.PermissionResponse
	chatID int64
}

const permissionRequestTTL = 5 * time.Minute

// Dispatcher routes inbound messages to the AgentService.
type Dispatcher struct {
	agentSvc acp.AgentService
	cfg      Config
	ctx      context.Context
	cancel   context.CancelFunc

	convMu             sync.Map // chatID (string) -> *sync.Mutex
	implicitStartLocks sync.Map // chatID (int64) -> *sync.Mutex
	cancelRequested    sync.Map // chatID (int64) -> struct{}

	pendingByChat map[int64]*pendingPrompt
	pendingMu     sync.Mutex

	pendingPerms  sync.Map // reqID -> *pendingPermission
	permActions   map[string][]acp.PermissionDecision
	permActionsMu sync.Mutex

	pendingResumeChoices map[int64][]acp.SessionInfo
	resumeChoicesMu      sync.Mutex

	activeResponders sync.Map // chatID (int64) -> channel.Responder

	memorySvc *memory.Service
}

// New creates a new Dispatcher.
func New(cfg Config) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		cfg:                  cfg,
		ctx:                  ctx,
		cancel:               cancel,
		permActions:          make(map[string][]acp.PermissionDecision),
		pendingByChat:        make(map[int64]*pendingPrompt),
		pendingResumeChoices: make(map[int64][]acp.SessionInfo),
	}
}

// Shutdown cancels the dispatcher context.
func (d *Dispatcher) Shutdown() {
	d.cancel()
}

// SetMemoryService sets the MemoryService for the Dispatcher.
func (d *Dispatcher) SetMemoryService(svc *memory.Service) {
	d.memorySvc = svc
}

// SetAgentService sets the AgentService for the Dispatcher.
func (d *Dispatcher) SetAgentService(svc acp.AgentService) {
	d.agentSvc = svc
	if svc != nil {
		d.setupCallbacks()
	}
}

func (d *Dispatcher) getOrCreateMutex(chatID string) *sync.Mutex {
	v, _ := d.convMu.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (d *Dispatcher) implicitStartMutex(chatID int64) *sync.Mutex {
	v, _ := d.implicitStartLocks.LoadOrStore(chatID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func parseChatID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

func randomToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Handle is the MessageHandler registered on each Channel.
func (d *Dispatcher) Handle(msg channel.InboundMessage, resp channel.Responder) {
	if cmd := parseCommand(msg.Text); cmd != "" {
		d.execCommand(cmd, msg, resp)
		return
	}

	chatID, ok := parseChatID(msg.ChatID)
	if !ok {
		slog.Error("invalid chatID", "chat_id", msg.ChatID)
		return
	}

	if d.agentSvc == nil {
		resp.Reply(channel.OutboundMessage{Text: "Agent not configured."}) //nolint:errcheck
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
				resp.Reply(channel.OutboundMessage{Text: "❌ Failed to start session."}) //nolint:errcheck
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

func (d *Dispatcher) queueBusyPrompt(chatID int64, input acp.PromptInput, resp channel.Responder, replyToMsgID int) {
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

func (d *Dispatcher) popPending(chatID int64) *pendingPrompt {
	d.pendingMu.Lock()
	p := d.pendingByChat[chatID]
	delete(d.pendingByChat, chatID)
	d.pendingMu.Unlock()
	return p
}

func (d *Dispatcher) runPromptLoop(ctx context.Context, chatID int64, input acp.PromptInput, resp channel.Responder) {
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

func (d *Dispatcher) appendUserToHistory(chatID int64, text string) {
	if d.memorySvc == nil || text == "" {
		return
	}
	if err := d.memorySvc.AppendHistory(strconv.FormatInt(chatID, 10), "user", text); err != nil {
		slog.Warn("failed to append user message to history", "chat_id", chatID, "error", err)
	}
}

func (d *Dispatcher) handlePromptResult(chatID int64, resp channel.Responder, reply *acp.AgentReply, err error) {
	if err != nil {
		if _, wasCancelled := d.cancelRequested.LoadAndDelete(chatID); !wasCancelled {
			d.sendPromptError(chatID, resp, err)
		}
	}
	if d.hasReplyContent(reply) {
		if err == nil && d.memorySvc != nil && reply.Text != "" {
			if appendErr := d.memorySvc.AppendHistory(strconv.FormatInt(chatID, 10), "assistant", reply.Text); appendErr != nil {
				slog.Warn("failed to append assistant reply to history", "chat_id", chatID, "error", appendErr)
			}
		}
		d.sendReply(resp, reply)
	}
}

func (d *Dispatcher) hasReplyContent(reply *acp.AgentReply) bool {
	return reply != nil && (reply.Text != "" || len(reply.Images) > 0 || len(reply.Files) > 0)
}

func (d *Dispatcher) sendPromptError(chatID int64, resp channel.Responder, err error) {
	switch {
	case errors.Is(err, acp.ErrAgentOutputLimitExceeded):
		resp.Reply(channel.OutboundMessage{ //nolint:errcheck
			Text: "Agent output exceeded ACP stdio limit. Restart with a higher `--acp-stdio-limit` (or `ACP_STDIO_LIMIT`).",
		})
	case errors.Is(err, acp.ErrNoActiveSession):
		resp.Reply(channel.OutboundMessage{ //nolint:errcheck
			Text: "No active session. Send a message again or use /new [workspace].",
		})
	default:
		slog.Error("prompt failed", "chat_id", chatID, "error", err)
		resp.Reply(channel.OutboundMessage{Text: "❌ Failed to process your request."}) //nolint:errcheck
	}
}

func (d *Dispatcher) sendReply(resp channel.Responder, reply *acp.AgentReply) {
	resp.Reply(convertToOutbound(reply)) //nolint:errcheck
}

func convertToPromptInput(msg channel.InboundMessage) acp.PromptInput {
	input := acp.PromptInput{Text: msg.Text}
	for _, att := range msg.Attachments {
		switch att.MediaType {
		case "image":
			input.Images = append(input.Images, acp.ImageData{
				MIMEType: "image/jpeg",
				Data:     att.Data,
				Name:     att.FileName,
			})
		default:
			fd := acp.FileData{
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

func convertToOutbound(reply *acp.AgentReply) channel.OutboundMessage {
	out := channel.OutboundMessage{Text: reply.Text}
	for _, img := range reply.Images {
		out.Images = append(out.Images, channel.ImageData{
			MIMEType: img.MIMEType,
			Data:     img.Data,
			Name:     img.Name,
		})
	}
	for _, f := range reply.Files {
		out.Files = append(out.Files, channel.FileData{
			MIMEType:    f.MIMEType,
			Data:        f.Data,
			Name:        f.Name,
			TextContent: f.TextContent,
		})
	}
	return out
}

func (d *Dispatcher) setupCallbacks() {
	d.agentSvc.SetPermissionHandler(func(chatID int64, req acp.PermissionRequest) <-chan acp.PermissionResponse {
		ch := make(chan acp.PermissionResponse, 1)
		pp := &pendingPermission{ch: ch, chatID: chatID}
		d.pendingPerms.Store(req.ID, pp)

		d.permActionsMu.Lock()
		d.permActions[req.ID] = req.AvailableActions
		d.permActionsMu.Unlock()

		go d.expirePermissionRequest(req.ID, ch)

		if v, ok := d.activeResponders.Load(chatID); ok {
			resp := v.(channel.Responder)
			resp.ShowPermissionUI(channel.PermissionRequest{ //nolint:errcheck
				ID:               req.ID,
				Tool:             req.Tool,
				Description:      req.Description,
				AvailableActions: permDecisionsToStrings(req.AvailableActions),
			})
		}
		return ch
	})

	d.agentSvc.SetActivityHandler(func(chatID int64, block acp.ActivityBlock) {
		if v, ok := d.activeResponders.Load(chatID); ok {
			resp := v.(channel.Responder)
			workspace := ""
			if info := d.agentSvc.ActiveSession(chatID); info != nil {
				workspace = info.Workspace
			}
			resp.SendActivity(channel.ActivityBlock{ //nolint:errcheck
				Kind:      string(block.Kind),
				Label:     block.Label,
				Detail:    block.Detail,
				Text:      block.Text,
				Status:    block.Status,
				Workspace: workspace,
			})
		}
	})
}

func (d *Dispatcher) expirePermissionRequest(reqID string, ch chan acp.PermissionResponse) {
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
			case ch <- acp.PermissionResponse{Decision: acp.PermissionDeny}:
			default:
			}
		}
	}
	d.permActionsMu.Lock()
	delete(d.permActions, reqID)
	d.permActionsMu.Unlock()
}

func permDecisionsToStrings(decisions []acp.PermissionDecision) []string {
	out := make([]string, len(decisions))
	for i, dec := range decisions {
		switch dec {
		case acp.PermissionAlways:
			out[i] = "always_allow"
		case acp.PermissionThisTime:
			out[i] = "allow_once"
		case acp.PermissionDeny:
			out[i] = "deny"
		default:
			out[i] = string(dec)
		}
	}
	return out
}

// RespondPermission resolves a pending permission request.
func (d *Dispatcher) RespondPermission(reqID string, decision acp.PermissionDecision) {
	if v, ok := d.pendingPerms.LoadAndDelete(reqID); ok {
		pp := v.(*pendingPermission)
		select {
		case pp.ch <- acp.PermissionResponse{Decision: decision}:
		default:
		}
		if decision == acp.PermissionAlways && d.agentSvc != nil {
			d.agentSvc.SetSessionPermissionMode(pp.chatID, acp.PermissionModeApprove)
		}
	}
	d.permActionsMu.Lock()
	delete(d.permActions, reqID)
	d.permActionsMu.Unlock()
}

// PermissionActions returns available actions for a pending permission request.
func (d *Dispatcher) PermissionActions(reqID string) []acp.PermissionDecision {
	d.permActionsMu.Lock()
	defer d.permActionsMu.Unlock()
	return d.permActions[reqID]
}

// HandleBusySendNow is called by Channel when "Send now" button is clicked.
func (d *Dispatcher) HandleBusySendNow(chatID int64, token string) (ok bool, err error) {
	d.pendingMu.Lock()
	p := d.pendingByChat[chatID]
	if p == nil || p.token != token {
		d.pendingMu.Unlock()
		return false, nil
	}
	d.pendingMu.Unlock()

	d.cancelRequested.Store(chatID, struct{}{})
	if err := d.agentSvc.Cancel(d.ctx, chatID); err != nil {
		d.cancelRequested.Delete(chatID)
		return false, err
	}
	return true, nil
}

// ResolveResumeChoice resolves a pending resume keyboard selection.
func (d *Dispatcher) ResolveResumeChoice(ctx context.Context, chatID int64, index int) (*acp.SessionInfo, error) {
	d.resumeChoicesMu.Lock()
	candidates := d.pendingResumeChoices[chatID]
	delete(d.pendingResumeChoices, chatID)
	d.resumeChoicesMu.Unlock()

	if candidates == nil {
		return nil, fmt.Errorf("selection expired")
	}
	if index < 0 || index >= len(candidates) {
		return nil, fmt.Errorf("invalid selection")
	}
	s := candidates[index]
	if err := d.agentSvc.LoadSession(ctx, chatID, s.SessionID, s.Workspace); err != nil {
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
