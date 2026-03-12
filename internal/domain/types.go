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
