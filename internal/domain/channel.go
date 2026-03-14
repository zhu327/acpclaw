package domain

// InboundMessage is the cross-channel unified inbound message format.
type InboundMessage struct {
	ChatRef
	ID          string
	Text        string
	AuthorID    string
	AuthorName  string
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

// Replier is the minimal interface for sending replies.
type Replier interface {
	Reply(msg OutboundMessage) error
}

// Responder extends Replier with UI and notification capabilities.
type Responder interface {
	Replier
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
