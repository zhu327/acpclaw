package acpclient

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/domain"
)

var _ acpsdk.Client = (*AcpClient)(nil)

func isPunctEnd(b byte) bool {
	switch b {
	case '.', '!', '?', ';', ':', ')', ']', '}':
		return true
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isNumericDotContinuation(chunks []string, next string) bool {
	if len(chunks) == 0 || len(next) < 2 {
		return false
	}
	prev := chunks[len(chunks)-1]
	if len(prev) < 2 || prev[len(prev)-1] != '.' || !isDigit(prev[len(prev)-2]) {
		return false
	}
	if !isDigit(next[0]) {
		return false
	}
	return isDigit(next[1]) || next[1] == '.'
}

func appendTextChunk(chunks []string, text string) []string {
	if len(chunks) == 0 || len(text) == 0 {
		return append(chunks, text)
	}
	prev := chunks[len(chunks)-1]
	if len(prev) == 0 {
		return append(chunks, text)
	}
	lastByte, firstByte := prev[len(prev)-1], text[0]
	if lastByte <= ' ' || firstByte <= ' ' {
		return append(chunks, text)
	}
	needsSpace := isPunctEnd(lastByte) && isAlphanumeric(firstByte) && !isNumericDotContinuation(chunks, text)
	if needsSpace {
		return append(chunks, " ", text)
	}
	return append(chunks, text)
}

// ActivityLabel returns a human-readable label for an activity kind and tool name.
func ActivityLabel(kind, toolName string) string {
	switch kind {
	case "think":
		return "💡 Thinking"
	case "execute":
		return "⚙️ Running"
	case "read":
		return "📖 Reading"
	case "edit":
		return "✏️ Editing"
	case "write":
		return "✍️ Writing"
	case "search":
		tl := strings.ToLower(toolName)
		if strings.Contains(tl, "web") || strings.Contains(tl, "browser") {
			return "🌐 Searching web"
		}
		if strings.Contains(tl, "local") || strings.Contains(tl, "code") || strings.Contains(tl, "search") {
			return "🔎 Querying project"
		}
		return "🔎 Querying"
	default:
		return "⚙️ Running"
	}
}

// InferActivityKind infers domain.ActivityKind from a tool name.
func InferActivityKind(toolName string) domain.ActivityKind {
	tl := strings.ToLower(toolName)
	switch {
	case tl == "think":
		return domain.ActivityThink
	case strings.Contains(tl, "read") || strings.Contains(tl, "view"):
		return domain.ActivityRead
	case strings.Contains(tl, "edit") || strings.Contains(tl, "replace") || strings.Contains(tl, "str_replace"):
		return domain.ActivityEdit
	case strings.Contains(tl, "write") || strings.Contains(tl, "create"):
		return domain.ActivityWrite
	case strings.Contains(tl, "search") || strings.Contains(tl, "find") || strings.Contains(tl, "grep"):
		return domain.ActivitySearch
	default:
		return domain.ActivityExecute
	}
}

// AcpClient implements acpsdk.Client and captures agent output for forwarding to channels.
type AcpClient struct {
	mu                 sync.Mutex
	textBuf            strings.Builder
	images             []domain.ImageData
	files              []domain.FileData
	activities         []domain.ActivityBlock
	activeBlock        *domain.ActivityBlock
	activeBlockChunks  []string
	activeToolCallID   acpsdk.ToolCallId
	pendingNonToolText []string

	onActivity   func(domain.ActivityBlock)
	onPermission func(domain.PermissionRequest) <-chan domain.PermissionResponse

	terminals *TerminalManager
}

// NewAcpClient creates a new AcpClient with the given callbacks.
func NewAcpClient(
	onActivity func(domain.ActivityBlock),
	onPermission func(domain.PermissionRequest) <-chan domain.PermissionResponse,
) *AcpClient {
	return &AcpClient{
		onActivity:   onActivity,
		onPermission: onPermission,
		terminals:    NewTerminalManager(),
	}
}

// ReleaseSessionTerminals kills and drops all terminals tracked for sessionID.
func (c *AcpClient) ReleaseSessionTerminals(sessionID string) {
	if c.terminals == nil {
		return
	}
	c.terminals.ReleaseSession(sessionID)
}

// StopTerminals stops the background terminal reaper (e.g. on client teardown).
func (c *AcpClient) StopTerminals() {
	if c.terminals == nil {
		return
	}
	c.terminals.Stop()
}

// SetCallbacks atomically replaces both activity and permission callbacks.
func (c *AcpClient) SetCallbacks(
	onActivity func(domain.ActivityBlock),
	onPermission func(domain.PermissionRequest) <-chan domain.PermissionResponse,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onActivity = onActivity
	c.onPermission = onPermission
}

// StartCapture resets the capture buffer for a new prompt.
func (c *AcpClient) StartCapture() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.textBuf.Reset()
	c.images = nil
	c.files = nil
	c.activities = nil
	c.activeBlock = nil
	c.activeBlockChunks = nil
	c.activeToolCallID = ""
	c.pendingNonToolText = nil
}

// FinishCapture returns the buffered reply.
func (c *AcpClient) FinishCapture() *domain.AgentReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appendPendingNonToolTextToReply()
	if c.activeBlock != nil {
		c.closeAndCollectActiveBlock("in_progress", true)
	}
	return &domain.AgentReply{
		Text:       c.textBuf.String(),
		Images:     c.images,
		Files:      c.files,
		Activities: c.activities,
	}
}

func (c *AcpClient) appendPendingNonToolTextToReply() {
	if len(c.pendingNonToolText) == 0 {
		return
	}
	text := strings.TrimSpace(strings.Join(c.pendingNonToolText, ""))
	c.pendingNonToolText = nil
	if text != "" {
		c.textBuf.WriteString(text)
	}
}

// SessionUpdate implements acpsdk.Client.
func (c *AcpClient) SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error {
	c.mu.Lock()
	u := params.Update

	var pendingActivities []*domain.ActivityBlock
	switch {
	case u.AgentMessageChunk != nil:
		c.appendContent(u.AgentMessageChunk.Content, false)
	case u.AgentThoughtChunk != nil:
		c.appendContent(u.AgentThoughtChunk.Content, false)
	case u.ToolCall != nil:
		pendingActivities = c.openToolBlock(u.ToolCall)
	case u.ToolCallUpdate != nil:
		pendingActivities = c.updateToolBlock(u.ToolCallUpdate)
	}

	onActivity := c.onActivity
	c.mu.Unlock()

	if onActivity != nil {
		for _, a := range pendingActivities {
			onActivity(*a)
		}
	}
	return nil
}

func (c *AcpClient) appendContent(block acpsdk.ContentBlock, fromTool bool) {
	switch {
	case block.Text != nil:
		c.appendText(block.Text.Text, fromTool)
	case block.Image != nil:
		data, err := base64.StdEncoding.DecodeString(block.Image.Data)
		if err != nil {
			slog.Warn("failed to decode image base64 data", "error", err)
			return
		}
		c.images = append(c.images, domain.ImageData{
			MIMEType: block.Image.MimeType,
			Data:     data,
			Name:     "",
		})
	case block.Resource != nil:
		c.appendResource(block.Resource.Resource)
	}
}

func ptrStr(s *string, def string) string {
	if s != nil {
		return *s
	}
	return def
}

func (c *AcpClient) appendResource(res acpsdk.EmbeddedResourceResource) {
	switch {
	case res.TextResourceContents != nil:
		t := res.TextResourceContents
		c.files = append(c.files, domain.FileData{
			MIMEType: ptrStr(t.MimeType, "text/plain"),
			Data:     []byte(t.Text),
			Name:     t.Uri,
		})
	case res.BlobResourceContents != nil:
		b := res.BlobResourceContents
		data, err := base64.StdEncoding.DecodeString(b.Blob)
		if err != nil {
			slog.Warn("failed to decode blob base64 data", "uri", b.Uri, "error", err)
			return
		}
		c.files = append(c.files, domain.FileData{
			MIMEType: ptrStr(b.MimeType, "application/octet-stream"),
			Data:     data,
			Name:     b.Uri,
		})
	}
}

func (c *AcpClient) appendText(text string, fromTool bool) {
	if text == "" {
		return
	}
	if c.activeBlock != nil {
		c.activeBlockChunks = appendTextChunk(c.activeBlockChunks, text)
		return
	}
	c.pendingNonToolText = appendTextChunk(c.pendingNonToolText, text)
}

func (c *AcpClient) flushPendingNonToolText() *domain.ActivityBlock {
	if len(c.pendingNonToolText) == 0 {
		return nil
	}
	text := strings.TrimSpace(strings.Join(c.pendingNonToolText, ""))
	c.pendingNonToolText = nil
	if text == "" {
		return nil
	}
	block := domain.ActivityBlock{
		Kind:   domain.ActivityThink,
		Label:  "💡 Thinking",
		Status: "completed",
		Text:   text,
	}
	c.activities = append(c.activities, block)
	return &block
}

func shouldEmitClosedActivity(block *domain.ActivityBlock) bool {
	if block == nil {
		return false
	}
	if block.Kind == domain.ActivityThink {
		return true
	}
	return block.Status == "completed" || block.Status == "failed"
}

func (c *AcpClient) closeAndCollectActiveBlock(status string, isPromptEnd bool) *domain.ActivityBlock {
	if c.activeBlock == nil {
		return nil
	}
	c.activeBlock.EndAt = time.Now()
	c.activeBlock.Status = status
	blockText := strings.TrimSpace(strings.Join(c.activeBlockChunks, ""))
	if isPromptEnd && c.activeBlock.Kind != domain.ActivityThink && blockText != "" {
		c.textBuf.WriteString(blockText)
		blockText = ""
	}
	c.activeBlock.Text = blockText
	closed := *c.activeBlock
	c.activities = append(c.activities, closed)
	c.activeBlock = nil
	c.activeBlockChunks = nil
	c.activeToolCallID = ""
	return &closed
}

func (c *AcpClient) openToolBlock(tc *acpsdk.SessionUpdateToolCall) []*domain.ActivityBlock {
	var emitted []*domain.ActivityBlock
	if think := c.flushPendingNonToolText(); think != nil {
		emitted = append(emitted, think)
	}
	if c.activeBlock != nil {
		closed := c.closeAndCollectActiveBlock("in_progress", false)
		if shouldEmitClosedActivity(closed) {
			emitted = append(emitted, closed)
		}
	}
	kind := toolKindToActivityKind(tc.Kind, tc.Title)
	label := ActivityLabel(string(kind), tc.Title)
	block := domain.ActivityBlock{
		Kind:    kind,
		Label:   label,
		Detail:  tc.Title,
		Status:  "in_progress",
		StartAt: time.Now(),
	}
	c.activeBlock = &block
	c.activeBlockChunks = nil
	c.activeToolCallID = tc.ToolCallId
	return emitted
}

func isToolCallTerminal(status *acpsdk.ToolCallStatus) bool {
	if status == nil {
		return false
	}
	s := *status
	return s == acpsdk.ToolCallStatusCompleted || s == acpsdk.ToolCallStatusFailed
}

func (c *AcpClient) updateToolBlock(tu *acpsdk.SessionToolCallUpdate) []*domain.ActivityBlock {
	var emitted []*domain.ActivityBlock
	for _, cont := range tu.Content {
		if cont.Content != nil {
			c.appendContent(cont.Content.Content, true)
		}
	}
	if !isToolCallTerminal(tu.Status) || c.activeBlock == nil || c.activeToolCallID != tu.ToolCallId {
		return emitted
	}
	status := "completed"
	if *tu.Status == acpsdk.ToolCallStatusFailed {
		status = "failed"
	}
	if closed := c.closeAndCollectActiveBlock(status, false); shouldEmitClosedActivity(closed) {
		emitted = append(emitted, closed)
	}
	return emitted
}

func toolKindToActivityKind(k acpsdk.ToolKind, title string) domain.ActivityKind {
	switch k {
	case acpsdk.ToolKindThink:
		return domain.ActivityThink
	case acpsdk.ToolKindRead:
		return domain.ActivityRead
	case acpsdk.ToolKindEdit:
		return domain.ActivityEdit
	case acpsdk.ToolKindSearch:
		return domain.ActivitySearch
	case acpsdk.ToolKindExecute:
		return domain.ActivityExecute
	case acpsdk.ToolKindOther, "":
		return InferActivityKind(title)
	default:
		return domain.ActivityExecute
	}
}

func availableActionsFromOptions(options []acpsdk.PermissionOption) []domain.PermissionDecision {
	hasAlways := false
	hasOnce := false
	for _, opt := range options {
		switch opt.Kind {
		case acpsdk.PermissionOptionKindAllowAlways:
			hasAlways = true
		case acpsdk.PermissionOptionKindAllowOnce:
			hasOnce = true
		}
	}
	var actions []domain.PermissionDecision
	if hasAlways {
		actions = append(actions, domain.PermissionAlways)
	}
	if hasOnce {
		actions = append(actions, domain.PermissionThisTime)
	}
	actions = append(actions, domain.PermissionDeny)
	return actions
}

// RequestPermission implements acpsdk.Client.
func (c *AcpClient) RequestPermission(
	ctx context.Context,
	req acpsdk.RequestPermissionRequest,
) (acpsdk.RequestPermissionResponse, error) {
	tool := ptrStr(req.ToolCall.Title, "")
	input := make(map[string]any)
	if m, ok := req.ToolCall.RawInput.(map[string]any); ok {
		input = m
	}
	ourReq := domain.PermissionRequest{
		ID:               string(req.ToolCall.ToolCallId),
		Tool:             tool,
		Description:      tool,
		Input:            input,
		AvailableActions: availableActionsFromOptions(req.Options),
	}

	c.mu.Lock()
	handler := c.onPermission
	c.mu.Unlock()

	if handler == nil {
		return permissionResponseToSDK(domain.PermissionResponse{Decision: domain.PermissionDeny}, req.Options), nil
	}
	ch := handler(ourReq)
	if ch == nil {
		return permissionResponseToSDK(domain.PermissionResponse{Decision: domain.PermissionDeny}, req.Options), nil
	}
	select {
	case resp := <-ch:
		return permissionResponseToSDK(resp, req.Options), nil
	case <-ctx.Done():
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.NewRequestPermissionOutcomeCancelled(),
		}, ctx.Err()
	}
}

func selectOption(
	options []acpsdk.PermissionOption,
	kind acpsdk.PermissionOptionKind,
) *acpsdk.RequestPermissionResponse {
	for _, opt := range options {
		if opt.Kind == kind {
			return &acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
				},
			}
		}
	}
	return nil
}

func denyResponse(options []acpsdk.PermissionOption) acpsdk.RequestPermissionResponse {
	if r := selectOption(options, acpsdk.PermissionOptionKindRejectOnce); r != nil {
		return *r
	}
	if r := selectOption(options, acpsdk.PermissionOptionKindRejectAlways); r != nil {
		return *r
	}
	if len(options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: options[len(options)-1].OptionId},
			},
		}
	}
	return acpsdk.RequestPermissionResponse{}
}

func allowResponse(options []acpsdk.PermissionOption, preferAlways bool) acpsdk.RequestPermissionResponse {
	kinds := []acpsdk.PermissionOptionKind{
		acpsdk.PermissionOptionKindAllowOnce,
	}
	if preferAlways {
		kinds = append([]acpsdk.PermissionOptionKind{acpsdk.PermissionOptionKindAllowAlways}, kinds...)
	}
	for _, k := range kinds {
		if r := selectOption(options, k); r != nil {
			return *r
		}
	}
	if len(options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: options[0].OptionId},
			},
		}
	}
	return acpsdk.RequestPermissionResponse{}
}

func permissionResponseToSDK(
	resp domain.PermissionResponse,
	options []acpsdk.PermissionOption,
) acpsdk.RequestPermissionResponse {
	switch resp.Decision {
	case domain.PermissionDeny:
		return denyResponse(options)
	case domain.PermissionThisTime:
		if r := selectOption(options, acpsdk.PermissionOptionKindAllowOnce); r != nil {
			return *r
		}
		return denyResponse(options)
	case domain.PermissionAlways:
		return allowResponse(options, true)
	default:
		return denyResponse(options)
	}
}

// ErrNotSupported is returned by AcpClient methods that are not implemented.
var ErrNotSupported = errors.New("not supported by this client")

// ReadTextFile implements acpsdk.Client.
func (c *AcpClient) ReadTextFile(_ context.Context, _ acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, ErrNotSupported
}

// WriteTextFile implements acpsdk.Client.
func (c *AcpClient) WriteTextFile(
	_ context.Context,
	_ acpsdk.WriteTextFileRequest,
) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, ErrNotSupported
}

// CreateTerminal implements acpsdk.Client.
func (c *AcpClient) CreateTerminal(
	_ context.Context,
	params acpsdk.CreateTerminalRequest,
) (acpsdk.CreateTerminalResponse, error) {
	return c.terminals.Create(params)
}

// KillTerminalCommand implements acpsdk.Client.
func (c *AcpClient) KillTerminalCommand(
	_ context.Context,
	params acpsdk.KillTerminalCommandRequest,
) (acpsdk.KillTerminalCommandResponse, error) {
	return c.terminals.Kill(params)
}

// TerminalOutput implements acpsdk.Client.
func (c *AcpClient) TerminalOutput(
	_ context.Context,
	params acpsdk.TerminalOutputRequest,
) (acpsdk.TerminalOutputResponse, error) {
	return c.terminals.Output(params)
}

// ReleaseTerminal implements acpsdk.Client.
func (c *AcpClient) ReleaseTerminal(
	_ context.Context,
	params acpsdk.ReleaseTerminalRequest,
) (acpsdk.ReleaseTerminalResponse, error) {
	return c.terminals.Release(params)
}

// WaitForTerminalExit implements acpsdk.Client.
func (c *AcpClient) WaitForTerminalExit(
	ctx context.Context,
	params acpsdk.WaitForTerminalExitRequest,
) (acpsdk.WaitForTerminalExitResponse, error) {
	return c.terminals.WaitForExit(ctx, params)
}
