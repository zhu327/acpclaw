package acp

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

var _ acpsdk.Client = (*AcpClient)(nil)

func isPunctEnd(b byte) bool {
	switch b {
	case '.', '!', '?', ';', ':', ')', ']', '}':
		return true
	}
	return false
}

func isNumericDotContinuation(chunks []string, next string) bool {
	if len(chunks) == 0 || len(next) < 2 {
		return false
	}
	prev := chunks[len(chunks)-1]
	if len(prev) < 2 || prev[len(prev)-1] != '.' {
		return false
	}
	if prev[len(prev)-2] < '0' || prev[len(prev)-2] > '9' {
		return false
	}
	if next[0] < '0' || next[0] > '9' {
		return false
	}
	return (next[1] >= '0' && next[1] <= '9') || next[1] == '.'
}

func appendTextChunk(chunks []string, text string) []string {
	if len(chunks) == 0 || len(text) == 0 {
		return append(chunks, text)
	}
	prev := chunks[len(chunks)-1]
	if len(prev) == 0 {
		return append(chunks, text)
	}
	lastByte := prev[len(prev)-1]
	firstByte := text[0]
	if lastByte <= ' ' || firstByte <= ' ' {
		return append(chunks, text)
	}
	if isPunctEnd(lastByte) &&
		((firstByte >= 'a' && firstByte <= 'z') || (firstByte >= 'A' && firstByte <= 'Z') ||
			(firstByte >= '0' && firstByte <= '9')) &&
		!isNumericDotContinuation(chunks, text) {
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

// InferActivityKind infers ActivityKind from a tool name.
func InferActivityKind(toolName string) ActivityKind {
	tl := strings.ToLower(toolName)
	switch {
	case tl == "think":
		return ActivityThink
	case strings.Contains(tl, "read") || strings.Contains(tl, "view"):
		return ActivityRead
	case strings.Contains(tl, "edit") || strings.Contains(tl, "replace") || strings.Contains(tl, "str_replace"):
		return ActivityEdit
	case strings.Contains(tl, "write") || strings.Contains(tl, "create"):
		return ActivityWrite
	case strings.Contains(tl, "search") || strings.Contains(tl, "find") || strings.Contains(tl, "grep"):
		return ActivitySearch
	default:
		return ActivityExecute
	}
}

// AcpClient implements acp.Client and captures agent output for forwarding to Telegram.
type AcpClient struct {
	mu                 sync.Mutex
	textBuf            strings.Builder
	images             []ImageData
	files              []FileData
	activities         []ActivityBlock
	activeBlock        *ActivityBlock
	activeBlockChunks  []string
	activeToolCallID   acpsdk.ToolCallId
	pendingNonToolText []string

	onActivity   func(ActivityBlock)
	onPermission func(PermissionRequest) <-chan PermissionResponse

	terminals *TerminalManager
}

// NewAcpClient creates a new AcpClient with the given callbacks.
func NewAcpClient(
	onActivity func(ActivityBlock),
	onPermission func(PermissionRequest) <-chan PermissionResponse,
) *AcpClient {
	return &AcpClient{
		onActivity:   onActivity,
		onPermission: onPermission,
		terminals:    NewTerminalManager(),
	}
}

// SetCallbacks atomically replaces both activity and permission callbacks.
func (c *AcpClient) SetCallbacks(
	onActivity func(ActivityBlock),
	onPermission func(PermissionRequest) <-chan PermissionResponse,
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
// Python parity: trailing pending non-tool text is appended to reply text directly;
// no think block is emitted at prompt end (only when a tool block opens).
func (c *AcpClient) FinishCapture() *AgentReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appendPendingNonToolTextToReply()
	if c.activeBlock != nil {
		c.closeAndCollectActiveBlock("in_progress", true)
	}
	return &AgentReply{
		Text:       c.textBuf.String(),
		Images:     c.images,
		Files:      c.files,
		Activities: c.activities,
	}
}

// appendPendingNonToolTextToReply appends pending non-tool text to textBuf without emitting a think block.
// Used at FinishCapture to match Python: final_text = "".join(chunks + pending_text).
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

// SessionUpdate implements acp.Client. Processes session updates and buffers output.
func (c *AcpClient) SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error {
	c.mu.Lock()
	u := params.Update

	var pendingActivities []*ActivityBlock
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
		} else {
			c.images = append(c.images, ImageData{
				MIMEType: block.Image.MimeType,
				Data:     data,
				Name:     "",
			})
		}
	case block.Resource != nil:
		c.appendResource(block.Resource.Resource)
	}
}

func (c *AcpClient) appendResource(res acpsdk.EmbeddedResourceResource) {
	switch {
	case res.TextResourceContents != nil:
		t := res.TextResourceContents
		mime := "text/plain"
		if t.MimeType != nil {
			mime = *t.MimeType
		}
		c.files = append(c.files, FileData{
			MIMEType: mime,
			Data:     []byte(t.Text),
			Name:     t.Uri,
		})
	case res.BlobResourceContents != nil:
		b := res.BlobResourceContents
		data, err := base64.StdEncoding.DecodeString(b.Blob)
		if err != nil {
			slog.Warn("failed to decode blob base64 data", "uri", b.Uri, "error", err)
		} else {
			mime := "application/octet-stream"
			if b.MimeType != nil {
				mime = *b.MimeType
			}
			c.files = append(c.files, FileData{
				MIMEType: mime,
				Data:     data,
				Name:     b.Uri,
			})
		}
	}
}

func (c *AcpClient) appendText(text string, fromTool bool) {
	if len(text) == 0 {
		return
	}
	if c.activeBlock != nil {
		c.activeBlockChunks = appendTextChunk(c.activeBlockChunks, text)
	} else {
		c.pendingNonToolText = appendTextChunk(c.pendingNonToolText, text)
	}
}

func (c *AcpClient) flushPendingNonToolText() *ActivityBlock {
	if len(c.pendingNonToolText) == 0 {
		return nil
	}
	text := strings.TrimSpace(strings.Join(c.pendingNonToolText, ""))
	c.pendingNonToolText = nil
	if text == "" {
		return nil
	}
	block := ActivityBlock{
		Kind:   ActivityThink,
		Label:  "💡 Thinking",
		Status: "completed",
		Text:   text,
	}
	c.activities = append(c.activities, block)
	return &block
}

func shouldEmitClosedActivity(block *ActivityBlock) bool {
	if block == nil {
		return false
	}
	if block.Kind == ActivityThink {
		return true
	}
	if strings.TrimSpace(block.Text) != "" {
		return true
	}
	return block.Status == "failed"
}

func (c *AcpClient) closeAndCollectActiveBlock(status string, isPromptEnd bool) *ActivityBlock {
	if c.activeBlock == nil {
		return nil
	}
	c.activeBlock.EndAt = time.Now()
	c.activeBlock.Status = status
	blockText := strings.TrimSpace(strings.Join(c.activeBlockChunks, ""))
	// Trailing non-think block: move text to reply instead of block.
	if isPromptEnd && c.activeBlock.Kind != ActivityThink && blockText != "" {
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

func (c *AcpClient) openToolBlock(tc *acpsdk.SessionUpdateToolCall) []*ActivityBlock {
	var emitted []*ActivityBlock
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
	block := ActivityBlock{
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

func (c *AcpClient) updateToolBlock(tu *acpsdk.SessionToolCallUpdate) []*ActivityBlock {
	var emitted []*ActivityBlock
	for _, cont := range tu.Content {
		if cont.Content != nil {
			c.appendContent(cont.Content.Content, true)
		}
	}
	if tu.Status != nil && (*tu.Status == acpsdk.ToolCallStatusCompleted || *tu.Status == acpsdk.ToolCallStatusFailed) {
		if c.activeBlock != nil && c.activeToolCallID == tu.ToolCallId {
			status := "completed"
			if *tu.Status == acpsdk.ToolCallStatusFailed {
				status = "failed"
			}
			closed := c.closeAndCollectActiveBlock(status, false)
			if shouldEmitClosedActivity(closed) {
				emitted = append(emitted, closed)
			}
		}
	}
	return emitted
}

func toolKindToActivityKind(k acpsdk.ToolKind, title string) ActivityKind {
	switch k {
	case acpsdk.ToolKindThink:
		return ActivityThink
	case acpsdk.ToolKindRead:
		return ActivityRead
	case acpsdk.ToolKindEdit:
		return ActivityEdit
	case acpsdk.ToolKindSearch:
		return ActivitySearch
	case acpsdk.ToolKindExecute:
		return ActivityExecute
	case acpsdk.ToolKindOther, "":
		return InferActivityKind(title)
	default:
		return ActivityExecute
	}
}

// availableActionsFromOptions computes available actions from SDK options (Python _available_actions parity).
// allow_always -> always; allow_once -> once; always append deny.
func availableActionsFromOptions(options []acpsdk.PermissionOption) []PermissionDecision {
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
	var actions []PermissionDecision
	if hasAlways {
		actions = append(actions, PermissionAlways)
	}
	if hasOnce {
		actions = append(actions, PermissionThisTime)
	}
	actions = append(actions, PermissionDeny)
	return actions
}

// RequestPermission implements acp.Client. Delegates to onPermission and maps SDK types.
func (c *AcpClient) RequestPermission(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	tool := ""
	desc := ""
	if req.ToolCall.Title != nil {
		tool = *req.ToolCall.Title
		desc = *req.ToolCall.Title
	}
	input := make(map[string]any)
	if req.ToolCall.RawInput != nil {
		if m, ok := req.ToolCall.RawInput.(map[string]any); ok {
			input = m
		}
	}
	ourReq := PermissionRequest{
		ID:               string(req.ToolCall.ToolCallId),
		Tool:             tool,
		Description:      desc,
		Input:            input,
		AvailableActions: availableActionsFromOptions(req.Options),
	}

	c.mu.Lock()
	handler := c.onPermission
	c.mu.Unlock()

	if handler == nil {
		return permissionResponseToSDK(PermissionResponse{Decision: PermissionDeny}, req.Options), nil
	}
	ch := handler(ourReq)
	if ch == nil {
		return permissionResponseToSDK(PermissionResponse{Decision: PermissionDeny}, req.Options), nil
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

func selectOption(options []acpsdk.PermissionOption, kind acpsdk.PermissionOptionKind) *acpsdk.RequestPermissionResponse {
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

func permissionResponseToSDK(resp PermissionResponse, options []acpsdk.PermissionOption) acpsdk.RequestPermissionResponse {
	switch resp.Decision {
	case PermissionDeny:
		return denyResponse(options)

	case PermissionThisTime:
		if r := selectOption(options, acpsdk.PermissionOptionKindAllowOnce); r != nil {
			return *r
		}
		return denyResponse(options)

	case PermissionAlways:
		if r := selectOption(options, acpsdk.PermissionOptionKindAllowAlways); r != nil {
			return *r
		}
		if r := selectOption(options, acpsdk.PermissionOptionKindAllowOnce); r != nil {
			return *r
		}
		if len(options) > 0 {
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: options[0].OptionId},
				},
			}
		}
		return acpsdk.RequestPermissionResponse{}

	default:
		return denyResponse(options)
	}
}

// ErrNotSupported is returned by AcpClient methods that are not implemented.
var ErrNotSupported = errors.New("not supported by this client")

// ReadTextFile implements acp.Client. Returns ErrNotSupported.
func (c *AcpClient) ReadTextFile(_ context.Context, _ acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, ErrNotSupported
}

// WriteTextFile implements acp.Client. Returns ErrNotSupported.
func (c *AcpClient) WriteTextFile(_ context.Context, _ acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, ErrNotSupported
}

// CreateTerminal implements acp.Client. Spawns a subprocess and returns its terminal ID.
func (c *AcpClient) CreateTerminal(_ context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return c.terminals.Create(params)
}

// KillTerminalCommand implements acp.Client. Sends SIGKILL to the terminal process.
func (c *AcpClient) KillTerminalCommand(_ context.Context, params acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error) {
	return c.terminals.Kill(params)
}

// TerminalOutput implements acp.Client. Returns current output and exit status.
func (c *AcpClient) TerminalOutput(_ context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return c.terminals.Output(params)
}

// ReleaseTerminal implements acp.Client. Kills the process and removes the terminal record.
func (c *AcpClient) ReleaseTerminal(_ context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return c.terminals.Release(params)
}

// WaitForTerminalExit implements acp.Client. Blocks until the terminal process exits.
func (c *AcpClient) WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return c.terminals.WaitForExit(ctx, params)
}
