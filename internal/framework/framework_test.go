package framework

import (
	"context"
	"testing"

	"github.com/zhu327/acpclaw/internal/domain"
)

type mockChannel struct {
	kind    string
	started bool
	handler domain.MessageHandler
}

func (c *mockChannel) Kind() string { return c.kind }
func (c *mockChannel) Start(ctx context.Context, handler domain.MessageHandler) error {
	c.started = true
	c.handler = handler
	return nil
}
func (c *mockChannel) Stop() error { return nil }

type channelPlugin struct {
	channels []domain.Channel
}

func (p *channelPlugin) Name() string               { return "test-channel" }
func (p *channelPlugin) Channels() []domain.Channel { return p.channels }

func TestFramework_Init_CollectsChannels(t *testing.T) {
	fw := New()
	ch := &mockChannel{kind: "test"}
	fw.Register(&channelPlugin{channels: []domain.Channel{ch}})

	if err := fw.Init(); err != nil {
		t.Fatal(err)
	}
	if _, ok := fw.channels["test"]; !ok {
		t.Fatal("channel 'test' not registered")
	}
}

type cmdPlugin struct {
	cmds []domain.Command
}

func (p *cmdPlugin) Name() string               { return "test-cmd" }
func (p *cmdPlugin) Commands() []domain.Command { return p.cmds }

type mockCommand struct {
	name string
}

func (c *mockCommand) Name() string        { return c.name }
func (c *mockCommand) Description() string { return "test" }
func (c *mockCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	return &domain.Result{Text: "ok"}, nil
}

func TestFramework_Init_CollectsCommands(t *testing.T) {
	fw := New()
	fw.Register(&cmdPlugin{cmds: []domain.Command{&mockCommand{name: "test"}}})

	if err := fw.Init(); err != nil {
		t.Fatal(err)
	}
	if _, ok := fw.commands["test"]; !ok {
		t.Fatal("command 'test' not registered")
	}
}
