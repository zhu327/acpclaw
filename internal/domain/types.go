package domain

// ImageData holds inline image content.
type ImageData struct {
	MIMEType string
	Data     []byte
	Name     string
}

// FileData holds inline file content.
// TextContent is set when the file is UTF-8 decodable (text file semantic); nil for binary.
type FileData struct {
	MIMEType    string
	Data        []byte
	Name        string
	TextContent *string // non-nil when UTF-8 decodable
}

// Attachment is a binary attachment from an IM channel (image, file, audio, etc.).
type Attachment struct {
	Data      []byte
	MediaType string // "image", "file", "audio"
	FileName  string
}

// ChatRef bundles channel kind and chat ID into a single type.
// Used throughout all interfaces instead of bare chatID string.
type ChatRef struct {
	ChannelKind string
	ChatID      string
}

// CompositeKey returns a globally unique key for map lookups and storage paths.
func (r ChatRef) CompositeKey() string {
	return r.ChannelKind + ":" + r.ChatID
}
