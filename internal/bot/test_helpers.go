package bot

import (
	"io"
	"net/http"
	"strings"
)

// mockHTTPTransport returns a fixed response for all requests (for testing bot API calls).
type mockHTTPTransport struct {
	statusCode int
	body       string
}

func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}
