package domain

// InboundMessage is the cross-channel unified inbound message format.
type InboundMessage struct {
	ID          string
	ChatID      string
	Text        string
	AuthorID    string
	AuthorName  string
	ChannelKind string
	Attachments []Attachment
}

// OutboundMessage is the cross-channel unified outbound message format.
type OutboundMessage struct {
	Text   string
	Images []ImageData
	Files  []FileData
}

// SessionChoice is used by Responder.ShowResumeKeyboard.
type SessionChoice struct {
	Index       int
	DisplayName string
}

// ChannelPermissionRequest is a cross-channel permission request (string-based decisions for UI display).
type ChannelPermissionRequest struct {
	ID               string
	Tool             string
	Description      string
	AvailableActions []string
}

// MessageHandler is the callback registered by Dispatcher on each Channel.
type MessageHandler func(msg InboundMessage, resp Responder)

// Responder encapsulates reply capabilities, bound to the IM context by the Channel.
type Responder interface {
	Reply(msg OutboundMessage) error
	ShowPermissionUI(req ChannelPermissionRequest) error
	ShowTypingIndicator() error
	SendActivity(block ActivityBlock) error
	ShowBusyNotification(token string, replyToMsgID int) (notifyMsgID int, err error)
	ClearBusyNotification(notifyMsgID int) error
	ShowResumeKeyboard(sessions []SessionChoice) error
}

// Channel is the minimal adapter interface for an IM platform.
type Channel interface {
	Kind() string
	Start(handler MessageHandler) error
	Stop() error
	Send(chatID string, msg OutboundMessage) error
}

// AllowlistChecker checks whether a user is allowed to interact with the bot.
type AllowlistChecker interface {
	IsAllowed(userID int64, username string) bool
}
