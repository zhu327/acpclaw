package util

import "io"

// NamedReader implements telegoapi.NamedReader (io.Reader + Name() string).
type NamedReader struct {
	FileName string
	R        io.Reader
}

func (n *NamedReader) Read(p []byte) (int, error) {
	return n.R.Read(p)
}

// Name returns the file name for this reader.
func (n *NamedReader) Name() string {
	return n.FileName
}
