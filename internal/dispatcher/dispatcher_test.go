package dispatcher_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/agent"
	"github.com/zhu327/acpclaw/internal/dispatcher"
	"github.com/zhu327/acpclaw/internal/domain"
)

type mockResponder struct {
	mu         sync.Mutex
	replies    []domain.OutboundMessage
	activities []domain.ActivityBlock
}

func (r *mockResponder) Reply(msg domain.OutboundMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, msg)
	return nil
}
func (r *mockResponder) ShowPermissionUI(_ domain.ChannelPermissionRequest) error { return nil }
func (r *mockResponder) ShowTypingIndicator() error                               { return nil }
func (r *mockResponder) SendActivity(block domain.ActivityBlock) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activities = append(r.activities, block)
	return nil
}
func (r *mockResponder) ShowBusyNotification(_ string, _ int) (int, error) { return 1, nil }
func (r *mockResponder) ClearBusyNotification(_ int) error                 { return nil }
func (r *mockResponder) ShowResumeKeyboard(_ []domain.SessionChoice) error { return nil }

func TestDispatcher_Handle_SlashNew(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{DefaultWorkspace: "/tmp"})
	svc := agent.NewEchoAgentService()
	d.SetAgentService(svc)

	resp := &mockResponder{}
	msg := domain.InboundMessage{ChatID: "100", Text: "/new", ChannelKind: "test"}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Session started")
}

func TestDispatcher_Handle_SlashStatus(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{})
	d.SetAgentService(agent.NewEchoAgentService())

	resp := &mockResponder{}
	msg := domain.InboundMessage{ChatID: "100", Text: "/status", ChannelKind: "test"}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Status")
}

func TestDispatcher_Handle_SlashHelp(t *testing.T) {
	d := dispatcher.New(dispatcher.Config{
		DefaultWorkspace: ".",
	})
	d.SetAgentService(nil) // no agent needed for /help

	resp := &mockResponder{}
	msg := domain.InboundMessage{
		ChatID:      "12345",
		Text:        "/help",
		ChannelKind: "test",
	}

	d.Handle(msg, resp)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "/new")
	assert.Contains(t, resp.replies[0].Text, "/help")
}
