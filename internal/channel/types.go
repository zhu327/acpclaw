package channel

// Channel is the minimal adapter interface for an IM platform.
type Channel interface {
	Kind() string
	Start(handler MessageHandler) error
	Stop() error
	Send(chatID string, msg OutboundMessage) error
}

// MessageHandler is the callback registered by Dispatcher on each Channel.
type MessageHandler func(msg InboundMessage, resp Responder)

// Responder encapsulates reply capabilities, bound to IM context by the Channel.
type Responder interface {
	Reply(msg OutboundMessage) error
	ShowPermissionUI(req PermissionRequest) error
	ShowTypingIndicator() error
	SendActivity(block ActivityBlock) error
	ShowBusyNotification(token string, replyToMsgID int) (notifyMsgID int, err error)
	ClearBusyNotification(notifyMsgID int) error
	ShowResumeKeyboard(sessions []SessionChoice) error
}

// InboundMessage is the cross-Channel unified inbound message format.
type InboundMessage struct {
	ID          string
	ChatID      string
	Text        string
	AuthorID    string
	AuthorName  string
	ChannelKind string
	Attachments []Attachment
}

// OutboundMessage is the cross-Channel unified outbound message format.
type OutboundMessage struct {
	Text   string
	Images []ImageData
	Files  []FileData
}

// ActivityBlock is a cross-Channel Agent activity block (tool execution status).
type ActivityBlock struct {
	Kind      string // "think", "execute", "read", "edit", "write", "search"
	Label     string
	Detail    string
	Text      string
	Status    string // "in_progress", "completed", "failed"
	Workspace string
}

// SessionChoice is used by Responder.ShowResumeKeyboard.
type SessionChoice struct {
	Index       int
	DisplayName string
}

// Attachment is a binary attachment (image, file, etc.).
type Attachment struct {
	Data      []byte
	MediaType string // "image", "file", "audio"
	FileName  string
}

// ImageData holds inline image content for outbound messages.
type ImageData struct {
	MIMEType string
	Data     []byte
	Name     string
}

// FileData holds inline file content for outbound messages.
type FileData struct {
	MIMEType    string
	Data        []byte
	Name        string
	TextContent *string
}

// PermissionRequest is a cross-Channel permission request.
type PermissionRequest struct {
	ID               string
	Tool             string
	Description      string
	AvailableActions []string
}

// PermissionResponse is the user's decision for a permission request.
type PermissionResponse struct {
	RequestID string
	Decision  string
}
