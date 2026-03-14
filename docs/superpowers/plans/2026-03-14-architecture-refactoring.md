# Architecture Refactoring Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor acpclaw to a plugin/hook-based architecture with Interface + Registry pattern, turn lifecycle pipeline, and clean separation of concerns.

**Architecture:** Framework orchestrates a turn lifecycle (ResolveSession → LoadContext → RouteMessage → ExecuteAction → SaveState → RenderOutbound → DispatchOutbound) via hook interfaces. BuiltinPlugin provides all current functionality. HookRegistry dispatches via type assertion with CallFirst/CallAll semantics.

**Tech Stack:** Go 1.25+, generics for type-safe hook dispatch, sync.Map for concurrent state.

**Spec:** `docs/superpowers/specs/2026-03-14-architecture-refactoring-design.md`

---

## File Structure

### New Files

| File | Responsibility |
|------|---------------|
| `internal/framework/registry.go` | Plugin interface, HookRegistry, CallFirst, CallAll |
| `internal/framework/registry_test.go` | Registry unit tests |
| `internal/framework/framework.go` | Framework struct, Register, Init, Start, ProcessInbound |
| `internal/framework/framework_test.go` | Framework pipeline tests |
| `internal/domain/hooks.go` | All hook interface definitions + turn types (State, Action, TurnContext, Result) |
| `internal/domain/command.go` | Command interface |

### Modified Files

| File | Change |
|------|--------|
| `internal/domain/types.go` | Add ChatRef struct |
| `internal/domain/channel.go` | InboundMessage embeds ChatRef, Channel adds ctx, Responder adds ChannelKind(), MessageHandler signature update |
| `internal/domain/agent.go` | Split AgentService → SessionManager + Prompter + PermissionHandler + ActivityObserver using ChatRef |

### Files Migrated Later (Phases 2-6)

| Current Location | New Location |
|-----------------|-------------|
| `internal/agent/` | `internal/builtin/agent/` |
| `internal/channel/telegram/` | `internal/builtin/channel/telegram/` |
| `internal/dispatcher/` | Decomposed into `internal/builtin/commands/` + `internal/builtin/plugin.go` |
| `internal/memory/` | `internal/builtin/memory/` |
| `internal/cron/` | `internal/builtin/cron/` |
| `internal/mcp/` | `internal/builtin/mcp/` |

---

## Chunk 1: Phase 1 — Skeleton (Domain Types, Hook Interfaces, Framework Core)

New code only. No changes to existing runtime behavior. After this chunk, `go build ./...` passes and all existing tests pass.

### Task 1: Add ChatRef to domain/types.go

**Files:**
- Modify: `internal/domain/types.go`

- [ ] **Step 1: Add ChatRef struct**

Add to the end of `internal/domain/types.go`:

```go
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
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/domain/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/domain/types.go
git commit -m "feat(domain): add ChatRef type for explicit channel+chat identity"
```

### Task 2: Create domain/hooks.go — Hook Interface Definitions

**Files:**
- Create: `internal/domain/hooks.go`

- [ ] **Step 1: Write the hook interfaces file**

```go
package domain

import "context"

// --- Turn Lifecycle Hooks ---
// Framework uses type assertion to detect which hooks a plugin implements.

// SessionResolver resolves a session ID from an inbound message.
// CallFirst semantics: first non-empty result wins.
type SessionResolver interface {
	ResolveSession(ctx context.Context, msg InboundMessage) (sessionID string, err error)
}

// ContextLoader loads state/context for a session before processing.
// CallAll semantics: all implementations called, each mutates the shared state.
type ContextLoader interface {
	LoadContext(ctx context.Context, sessionID string, state State) error
}

// MessageRouter decides whether a message is a command or a prompt.
// CallFirst semantics.
type MessageRouter interface {
	RouteMessage(ctx context.Context, msg InboundMessage, state State) (Action, error)
}

// ActionExecutor executes a routed action (command or prompt).
// CallFirst semantics.
type ActionExecutor interface {
	ExecuteAction(ctx context.Context, action Action, tc *TurnContext) (*Result, error)
}

// StateSaver persists state after a turn completes.
// CallAll semantics: all implementations called (in defer block).
type StateSaver interface {
	SaveState(ctx context.Context, sessionID string, state State) error
}

// OutboundRenderer converts a Result into channel-agnostic outbound messages.
// CallAll semantics: results concatenated.
type OutboundRenderer interface {
	RenderOutbound(ctx context.Context, result *Result, state State) ([]OutboundMessage, error)
}

// OutboundDispatcher sends outbound messages to channels.
// CallAll semantics.
type OutboundDispatcher interface {
	DispatchOutbound(ctx context.Context, msg OutboundMessage, resp Responder) error
}

// --- Provider Hooks ---

// ChannelProvider provides channel adapters.
// CallAll semantics: channels merged by Kind(), first wins.
type ChannelProvider interface {
	Channels() []Channel
}

// CommandProvider provides commands.
// CallAll semantics: commands merged by Name(), first wins.
type CommandProvider interface {
	Commands() []Command
}

// --- Callback Hooks ---

// BusyHandler handles the "send now" callback when a chat is busy.
// CallFirst semantics.
type BusyHandler interface {
	HandleBusySendNow(chat ChatRef, token string) (ok bool, err error)
}

// ResumeHandler handles session resume choice callbacks.
// CallFirst semantics.
type ResumeHandler interface {
	ResolveResumeChoice(ctx context.Context, chat ChatRef, sessionIndex int) (*SessionInfo, error)
}

// --- Observer Hooks ---

// ErrorObserver is notified when errors occur during turn processing.
// CallAll semantics, fault-isolated (one observer failure doesn't block others).
type ErrorObserver interface {
	OnError(ctx context.Context, stage string, err error, msg InboundMessage)
}

// --- Plugin Lifecycle ---

// PluginInitializer is implemented by plugins that need a reference to the Framework.
// The Framework type is defined in the framework package; plugins accept it as any
// and type-assert internally to avoid a circular import.
type PluginInitializer interface {
	Init(fw any) error
}
```

Note: `State`, `Action`, `TurnContext`, and `Result` are referenced here but defined in `framework/types.go`. To avoid circular imports, we define type aliases in domain:

Add to the bottom of `internal/domain/hooks.go`:

```go
// Type aliases for framework types used in hook interfaces.
// The canonical definitions live in internal/framework/types.go.
// These aliases break the import cycle: domain ← framework.
type State = map[string]any
```

Wait — this creates a problem. `Action`, `TurnContext`, and `Result` are defined in `framework/types.go` but referenced in `domain/hooks.go`. Since `framework` imports `domain`, `domain` cannot import `framework`.

**Resolution:** Move `State`, `Action`, `TurnContext`, and `Result` to `domain/` since they are part of the hook contracts. The `framework` package uses them directly from `domain`.

- [ ] **Step 1 (revised): Create domain/hooks.go with turn types included**

```go
package domain

import "context"

// --- Turn Types (used by hook interfaces) ---

// State holds per-turn mutable state passed through the lifecycle pipeline.
type State map[string]any

// ActionKind distinguishes between commands and prompts.
type ActionKind int

const (
	ActionCommand ActionKind = iota
	ActionPrompt
)

// Action represents a routed message — either a command or a prompt.
type Action struct {
	Kind    ActionKind
	Command string
	Args    []string
	Input   PromptInput
}

// Result holds the outcome of executing an action.
type Result struct {
	Reply            *AgentReply
	Text             string
	SuppressOutbound bool
}

// TurnContext aggregates all context needed during a turn.
type TurnContext struct {
	Chat      ChatRef
	SessionID string
	Message   InboundMessage
	Responder Responder
	State     State
}

// --- Turn Lifecycle Hooks ---

// SessionResolver resolves a session ID from an inbound message.
// CallFirst semantics: first non-empty result wins.
type SessionResolver interface {
	ResolveSession(ctx context.Context, msg InboundMessage) (sessionID string, err error)
}

// ContextLoader loads state/context for a session before processing.
// CallAll semantics: all implementations called, each mutates the shared state.
type ContextLoader interface {
	LoadContext(ctx context.Context, sessionID string, state State) error
}

// MessageRouter decides whether a message is a command or a prompt.
// CallFirst semantics.
type MessageRouter interface {
	RouteMessage(ctx context.Context, msg InboundMessage, state State) (Action, error)
}

// ActionExecutor executes a routed action (command or prompt).
// CallFirst semantics.
type ActionExecutor interface {
	ExecuteAction(ctx context.Context, action Action, tc *TurnContext) (*Result, error)
}

// StateSaver persists state after a turn completes.
// CallAll semantics: all implementations called (in defer block).
type StateSaver interface {
	SaveState(ctx context.Context, sessionID string, state State) error
}

// OutboundRenderer converts a Result into channel-agnostic outbound messages.
// CallAll semantics: results concatenated.
type OutboundRenderer interface {
	RenderOutbound(ctx context.Context, result *Result, state State) ([]OutboundMessage, error)
}

// OutboundDispatcher sends outbound messages to channels.
// CallAll semantics.
type OutboundDispatcher interface {
	DispatchOutbound(ctx context.Context, msg OutboundMessage, resp Responder) error
}

// --- Provider Hooks ---

// ChannelProvider provides channel adapters.
// CallAll semantics: channels merged by Kind(), first wins.
type ChannelProvider interface {
	Channels() []Channel
}

// CommandProvider provides commands.
// CallAll semantics: commands merged by Name(), first wins.
type CommandProvider interface {
	Commands() []Command
}

// --- Callback Hooks ---

// BusyHandler handles the "send now" callback when a chat is busy.
// CallFirst semantics.
type BusyHandler interface {
	HandleBusySendNow(chat ChatRef, token string) (ok bool, err error)
}

// ResumeHandler handles session resume choice callbacks.
// CallFirst semantics.
type ResumeHandler interface {
	ResolveResumeChoice(ctx context.Context, chat ChatRef, sessionIndex int) (*SessionInfo, error)
}

// --- Observer Hooks ---

// ErrorObserver is notified when errors occur during turn processing.
// CallAll semantics, fault-isolated (one observer failure doesn't block others).
type ErrorObserver interface {
	OnError(ctx context.Context, stage string, err error, msg InboundMessage)
}

// --- Plugin Lifecycle ---

// PluginInitializer is implemented by plugins that need a reference to the
// framework. Accepts any to avoid circular import; plugins type-assert internally.
type PluginInitializer interface {
	Init(fw any) error
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/domain/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/domain/hooks.go
git commit -m "feat(domain): add hook interfaces and turn types"
```

### Task 3: Create domain/command.go — Command Interface

**Files:**
- Create: `internal/domain/command.go`

- [ ] **Step 1: Write the Command interface**

```go
package domain

import "context"

// Command represents a user-invocable slash command (e.g. /new, /help).
type Command interface {
	Name() string
	Description() string
	Execute(ctx context.Context, args []string, tc *TurnContext) (*Result, error)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/domain/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/domain/command.go
git commit -m "feat(domain): add Command interface"
```

### Task 4: Update domain/channel.go — ChatRef embedding, interface updates

**Files:**
- Modify: `internal/domain/channel.go`

This task updates the interfaces for the new architecture. Since existing code (dispatcher, telegram channel, agent) still uses the old interfaces, we keep the old `AgentService` and existing types working. The new interfaces coexist with old ones until migration phases.

- [ ] **Step 1: Update InboundMessage to embed ChatRef**

Replace the `InboundMessage` struct in `internal/domain/channel.go`:

Old:
```go
type InboundMessage struct {
	ID          string
	ChatID      string
	Text        string
	AuthorID    string
	AuthorName  string
	ChannelKind string
	Attachments []Attachment
}
```

New:
```go
type InboundMessage struct {
	ChatRef
	ID          string
	Text        string
	AuthorID    string
	AuthorName  string
	Attachments []Attachment
}
```

- [ ] **Step 2: Fix compilation errors from ChatRef embedding**

The `InboundMessage` previously had `ChatID` and `ChannelKind` as direct fields. After embedding `ChatRef`, these become promoted fields with the same names, so existing code accessing `msg.ChatID` and `msg.ChannelKind` continues to work.

However, struct literal initialization changes. Find all places that construct `InboundMessage` with named fields and update them.

Run: `go build ./...` to find errors.

Update each call site:

**Critical constraint:** In Phase 1 we must NOT change the ChatID values. The dispatcher and agent use `msg.ChatID` as map keys. We only change the struct literal syntax (field names) to use the new `ChatRef` embedding. The ChatID value stays as the composite key (`"telegram:12345"`) for now. The split to raw channel-local ChatID happens in Phase 4 when the dispatcher is removed and all maps use `ChatRef.CompositeKey()`.

**Note on CompositeKey():** Existing code (dispatcher, agent) accesses `msg.ChatID` directly and never calls `CompositeKey()`. The Framework tests use raw ChatID values (e.g. `ChatRef{ChannelKind: "test", ChatID: "123"}`) since they test the new code path where `CompositeKey()` works correctly. The temporary inconsistency (`ChatRef` with composite ChatID value in old code) does not cause issues because old code never calls `CompositeKey()`.

**`cmd/acpclaw/app.go`** (cron `OnTrigger` callback where `InboundMessage` is constructed):

Old:
```go
msg := domain.InboundMessage{ChatID: job.Channel + ":" + job.ChatID, Text: job.Message}
```
New (same ChatID value, just different struct syntax):
```go
msg := domain.InboundMessage{ChatRef: domain.ChatRef{ChannelKind: job.Channel, ChatID: job.Channel + ":" + job.ChatID}, Text: job.Message}
```

**`internal/channel/telegram/channel.go`** (convertInbound):

Old:
```go
ChatID: "telegram:" + strconv.FormatInt(msg.Chat.ID, 10),
ChannelKind: "telegram",
```
New (same ChatID value, just different struct syntax):
```go
ChatRef: domain.ChatRef{ChannelKind: "telegram", ChatID: "telegram:" + strconv.FormatInt(msg.Chat.ID, 10)},
```

**Test files:** search for `domain.InboundMessage{` in test files and update literals similarly, preserving the existing ChatID values.

- [ ] **Step 3: Verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(domain): embed ChatRef in InboundMessage"
```

### Task 5: Split domain/agent.go — New interfaces alongside old AgentService

**Files:**
- Modify: `internal/domain/agent.go`

We add the new split interfaces (`SessionManager`, `Prompter`, `PermissionHandler`, `ActivityObserver`) using `ChatRef`. The old `AgentService` interface is kept for now — existing code still depends on it. It will be removed in Phase 6.

- [ ] **Step 1: Add split interfaces to domain/agent.go**

Add after the existing `AgentService` interface:

```go
// --- Split interfaces (new architecture) ---
// These coexist with AgentService during migration.
// AgentService will be removed in Phase 6.

// SessionManager handles session lifecycle.
type SessionManager interface {
	NewSession(ctx context.Context, chat ChatRef, workspace string) error
	LoadSession(ctx context.Context, chat ChatRef, sessionID, workspace string) error
	ListSessions(ctx context.Context, chat ChatRef) ([]SessionInfo, error)
	ActiveSession(chat ChatRef) *SessionInfo
	Reconnect(ctx context.Context, chat ChatRef, workspace string) error
	Shutdown()
}

// Prompter handles agent prompt execution.
type Prompter interface {
	Prompt(ctx context.Context, chat ChatRef, input PromptInput) (*AgentReply, error)
	Cancel(ctx context.Context, chat ChatRef) error
}

// PermissionHandler manages permission request wiring.
type PermissionHandler interface {
	SetPermissionHandler(fn func(chat ChatRef, req PermissionRequest) <-chan PermissionResponse)
	SetSessionPermissionMode(chat ChatRef, mode PermissionMode)
}

// ActivityObserver manages activity update wiring.
type ActivityObserver interface {
	SetActivityHandler(fn func(chat ChatRef, block ActivityBlock))
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/domain/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/domain/agent.go
git commit -m "feat(domain): add split agent interfaces (SessionManager, Prompter, etc.)"
```

### Task 6: Create framework/registry.go — HookRegistry with CallFirst/CallAll

**Files:**
- Create: `internal/framework/registry.go`
- Create: `internal/framework/registry_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/framework/registry_test.go`:

```go
package framework

import (
	"errors"
	"testing"
)

type testPlugin struct{ name string }

func (p testPlugin) Name() string { return p.name }

type greeter interface {
	Greet() (string, error)
}

type greeterPlugin struct {
	testPlugin
	greeting string
}

func (p greeterPlugin) Greet() (string, error) { return p.greeting, nil }

type counter interface {
	Count() error
}

type counterPlugin struct {
	testPlugin
	total *int
}

func (p counterPlugin) Count() error { *p.total++; return nil }

func TestCallFirst_ReturnsFirstNonEmpty(t *testing.T) {
	r := NewHookRegistry()
	r.Register(greeterPlugin{testPlugin{"a"}, ""})
	r.Register(greeterPlugin{testPlugin{"b"}, "hello"})
	r.Register(greeterPlugin{testPlugin{"c"}, ""})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Fatalf("expected 'hello', got %v", result)
	}
}

func TestCallFirst_SkipsNonImplementors(t *testing.T) {
	r := NewHookRegistry()
	r.Register(testPlugin{"plain"})
	r.Register(greeterPlugin{testPlugin{"g"}, "hi"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hi" {
		t.Fatalf("expected 'hi', got %v", result)
	}
}

func TestCallFirst_ReturnsNilWhenNoImplementor(t *testing.T) {
	r := NewHookRegistry()
	r.Register(testPlugin{"plain"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestCallFirst_LaterRegistrationWins(t *testing.T) {
	r := NewHookRegistry()
	r.Register(greeterPlugin{testPlugin{"first"}, "one"})
	r.Register(greeterPlugin{testPlugin{"second"}, "two"})

	result, err := CallFirst[greeter](r, func(h greeter) (any, error) {
		return h.Greet()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "two" {
		t.Fatalf("expected 'two' (later wins), got %v", result)
	}
}

func TestCallAll_CallsAllImplementors(t *testing.T) {
	total := 0
	r := NewHookRegistry()
	r.Register(counterPlugin{testPlugin{"a"}, &total})
	r.Register(testPlugin{"skip"})
	r.Register(counterPlugin{testPlugin{"b"}, &total})

	errs := CallAll[counter](r, func(h counter) error { return h.Count() })
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if total != 2 {
		t.Fatalf("expected 2 calls, got %d", total)
	}
}

func TestCallAll_CollectsErrors(t *testing.T) {
	r := NewHookRegistry()
	boom := errors.New("boom")
	r.Register(counterPlugin{testPlugin{"a"}, new(int)})

	errs := CallAll[counter](r, func(h counter) error { return boom })
	if len(errs) != 1 || !errors.Is(errs[0], boom) {
		t.Fatalf("expected [boom], got %v", errs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/framework/... -v`
Expected: FAIL (package doesn't exist yet)

- [ ] **Step 3: Write the implementation**

Create `internal/framework/registry.go`:

```go
package framework

import "github.com/zhu327/acpclaw/internal/domain"

// Plugin is the base interface all plugins must implement.
type Plugin interface {
	Name() string
}

// HookRegistry holds registered plugins and dispatches hook calls.
type HookRegistry struct {
	plugins []Plugin
}

// NewHookRegistry creates an empty registry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{}
}

// Register adds a plugin. Later registrations have higher priority.
func (r *HookRegistry) Register(p Plugin) {
	r.plugins = append(r.plugins, p)
}

// Plugins returns all registered plugins (for inspection/testing).
func (r *HookRegistry) Plugins() []Plugin {
	return r.plugins
}

// CallFirst iterates plugins in reverse registration order (latest first),
// calls fn on each that implements T, returns the first non-zero result.
// Returns (nil, nil) if no implementor returns a non-zero value.
func CallFirst[T any](r *HookRegistry, fn func(T) (any, error)) (any, error) {
	for i := len(r.plugins) - 1; i >= 0; i-- {
		if h, ok := r.plugins[i].(T); ok {
			result, err := fn(h)
			if err != nil {
				return nil, err
			}
			if result != nil && result != "" {
				return result, nil
			}
		}
	}
	return nil, nil
}

// CallAll iterates plugins in registration order, calls fn on each that
// implements T, collects and returns any errors.
func CallAll[T any](r *HookRegistry, fn func(T) error) []error {
	var errs []error
	for _, p := range r.plugins {
		if h, ok := p.(T); ok {
			if err := fn(h); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// CallAllFaultIsolated is like CallAll but catches panics in each call,
// used for ErrorObserver where one failure must not block others.
func CallAllFaultIsolated[T any](r *HookRegistry, fn func(T) error) {
	for _, p := range r.plugins {
		if h, ok := p.(T); ok {
			func() {
				defer func() { recover() }()
				_ = fn(h)
			}()
		}
	}
}

// InitPlugins calls Init(fw) on any plugin implementing domain.PluginInitializer.
func (r *HookRegistry) InitPlugins(fw any) error {
	for _, p := range r.plugins {
		if pi, ok := p.(domain.PluginInitializer); ok {
			if err := pi.Init(fw); err != nil {
				return err
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/framework/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/framework/registry.go internal/framework/registry_test.go
git commit -m "feat(framework): add HookRegistry with CallFirst/CallAll"
```

### Task 7: Create framework/framework.go — Framework Core

**Files:**
- Create: `internal/framework/framework.go`
- Create: `internal/framework/framework_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/framework/framework_test.go`:

```go
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
func (c *mockChannel) Start(handler domain.MessageHandler) error {
	c.started = true
	c.handler = handler
	return nil
}
func (c *mockChannel) Stop() error                                    { return nil }
func (c *mockChannel) Send(chatID string, msg domain.OutboundMessage) error { return nil }

type channelPlugin struct {
	channels []domain.Channel
}

func (p *channelPlugin) Name() string             { return "test-channel" }
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

func (p *cmdPlugin) Name() string              { return "test-cmd" }
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/framework/... -v -run TestFramework`
Expected: FAIL (Framework not defined)

- [ ] **Step 3: Write the implementation**

Create `internal/framework/framework.go`:

```go
package framework

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/zhu327/acpclaw/internal/domain"
	"golang.org/x/sync/errgroup"
)

// Framework orchestrates the turn lifecycle via registered plugins.
type Framework struct {
	registry     *HookRegistry
	channels     map[string]domain.Channel
	commands     map[string]domain.Command
	ctx          context.Context
	responders   sync.Map // compositeKey → domain.Responder
	pendingPerms sync.Map // reqID → *permEntry
}

type permEntry struct {
	ch   chan domain.PermissionResponse
	chat domain.ChatRef
}

// New creates a new Framework.
func New() *Framework {
	return &Framework{
		registry: NewHookRegistry(),
	}
}

// Register adds a plugin to the framework.
func (f *Framework) Register(p Plugin) {
	f.registry.Register(p)
}

// Init collects channels and commands from registered plugins,
// and calls PluginInitializer.Init on plugins that need it.
func (f *Framework) Init() error {
	if err := f.registry.InitPlugins(f); err != nil {
		return fmt.Errorf("plugin init: %w", err)
	}

	f.channels = make(map[string]domain.Channel)
	CallAll[domain.ChannelProvider](f.registry, func(cp domain.ChannelProvider) error {
		for _, ch := range cp.Channels() {
			if _, exists := f.channels[ch.Kind()]; !exists {
				f.channels[ch.Kind()] = ch
			}
		}
		return nil
	})

	f.commands = make(map[string]domain.Command)
	CallAll[domain.CommandProvider](f.registry, func(cp domain.CommandProvider) error {
		for _, cmd := range cp.Commands() {
			if _, exists := f.commands[cmd.Name()]; !exists {
				f.commands[cmd.Name()] = cmd
			}
		}
		return nil
	})

	return nil
}

// Start starts all registered channels and blocks until ctx is cancelled.
func (f *Framework) Start(ctx context.Context) error {
	if len(f.channels) == 0 {
		return fmt.Errorf("no channels registered")
	}
	f.ctx = ctx
	g, gCtx := errgroup.WithContext(ctx)
	for _, ch := range f.channels {
		ch := ch
		g.Go(func() error {
			handler := func(msg domain.InboundMessage, resp domain.Responder) {
				if err := f.ProcessInbound(gCtx, msg, resp); err != nil {
					slog.Error("ProcessInbound failed", "chat", msg.ChatRef.CompositeKey(), "error", err)
				}
			}
			return ch.Start(handler)
		})
	}
	return g.Wait()
}

// ProcessInbound executes the 7-step turn lifecycle pipeline.
func (f *Framework) ProcessInbound(ctx context.Context, msg domain.InboundMessage, resp domain.Responder) (retErr error) {
	key := msg.ChatRef.CompositeKey()
	f.responders.Store(key, resp)
	defer f.responders.Delete(key)

	defer func() {
		if retErr != nil {
			CallAllFaultIsolated[domain.ErrorObserver](f.registry, func(o domain.ErrorObserver) error {
				o.OnError(ctx, "turn", retErr, msg)
				return nil
			})
		}
	}()

	// 1. ResolveSession
	sessionResult, err := CallFirst[domain.SessionResolver](f.registry, func(h domain.SessionResolver) (any, error) {
		return h.ResolveSession(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("resolve session: %w", err)
	}
	sessionID, _ := sessionResult.(string)

	// 2. LoadContext
	state := make(domain.State)
	state["commands"] = f.commands
	errs := CallAll[domain.ContextLoader](f.registry, func(h domain.ContextLoader) error {
		return h.LoadContext(ctx, sessionID, state)
	})
	if len(errs) > 0 {
		slog.Warn("context loader errors", "errors", errs)
	}

	// 3. RouteMessage
	actionResult, err := CallFirst[domain.MessageRouter](f.registry, func(h domain.MessageRouter) (any, error) {
		return h.RouteMessage(ctx, msg, state)
	})
	if err != nil {
		return fmt.Errorf("route message: %w", err)
	}

	var action domain.Action
	if actionResult != nil {
		action = actionResult.(domain.Action)
	} else {
		action = domain.Action{
			Kind:  domain.ActionPrompt,
			Input: domain.PromptInput{Text: msg.Text},
		}
	}

	tc := &domain.TurnContext{
		Chat:      msg.ChatRef,
		SessionID: sessionID,
		Message:   msg,
		Responder: resp,
		State:     state,
	}

	// 5. SaveState (deferred, always runs)
	defer func() {
		saveErrs := CallAll[domain.StateSaver](f.registry, func(h domain.StateSaver) error {
			return h.SaveState(ctx, sessionID, state)
		})
		if len(saveErrs) > 0 {
			slog.Warn("state saver errors", "errors", saveErrs)
		}
	}()

	// 4. ExecuteAction
	var result *domain.Result
	if action.Kind == domain.ActionCommand {
		cmd, ok := f.commands[action.Command]
		if !ok {
			result = &domain.Result{Text: "Unknown command: /" + action.Command}
		} else {
			result, err = cmd.Execute(ctx, action.Args, tc)
			if err != nil {
				return fmt.Errorf("execute command %s: %w", action.Command, err)
			}
		}
	} else {
		execResult, err := CallFirst[domain.ActionExecutor](f.registry, func(h domain.ActionExecutor) (any, error) {
			return h.ExecuteAction(ctx, action, tc)
		})
		if err != nil {
			return fmt.Errorf("execute action: %w", err)
		}
		if execResult != nil {
			result = execResult.(*domain.Result)
		}
	}

	if result == nil {
		return nil
	}

	// 6. RenderOutbound
	if result.SuppressOutbound {
		return nil
	}

	var outbounds []domain.OutboundMessage
	if result.Text != "" {
		outbounds = append(outbounds, domain.OutboundMessage{Text: result.Text})
	}
	CallAll[domain.OutboundRenderer](f.registry, func(h domain.OutboundRenderer) error {
		msgs, err := h.RenderOutbound(ctx, result, state)
		if err != nil {
			return err
		}
		outbounds = append(outbounds, msgs...)
		return nil
	})

	// 7. DispatchOutbound
	for _, out := range outbounds {
		CallAll[domain.OutboundDispatcher](f.registry, func(h domain.OutboundDispatcher) error {
			return h.DispatchOutbound(ctx, out, resp)
		})
	}

	return nil
}

// GetResponder returns the active Responder for a chat, or nil if no turn is active.
func (f *Framework) GetResponder(chat domain.ChatRef) domain.Responder {
	v, ok := f.responders.Load(chat.CompositeKey())
	if !ok {
		return nil
	}
	return v.(domain.Responder)
}

// RespondPermission resolves a pending permission request.
func (f *Framework) RespondPermission(reqID string, decision domain.PermissionDecision) {
	v, ok := f.pendingPerms.LoadAndDelete(reqID)
	if !ok {
		return
	}
	pe := v.(*permEntry)
	select {
	case pe.ch <- domain.PermissionResponse{Decision: decision}:
	default:
	}
	if decision == domain.PermissionAlways {
		CallFirst[domain.PermissionHandler](f.registry, func(h domain.PermissionHandler) (any, error) {
			h.SetSessionPermissionMode(pe.chat, domain.PermissionModeApprove)
			return nil, nil
		})
	}
}

// HandleBusySendNow delegates to the BusyHandler hook.
func (f *Framework) HandleBusySendNow(chat domain.ChatRef, token string) (bool, error) {
	result, err := CallFirst[domain.BusyHandler](f.registry, func(h domain.BusyHandler) (any, error) {
		ok, err := h.HandleBusySendNow(chat, token)
		return ok, err
	})
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	return result.(bool), nil
}

// ResolveResumeChoice delegates to the ResumeHandler hook.
func (f *Framework) ResolveResumeChoice(ctx context.Context, chat domain.ChatRef, sessionIndex int) (*domain.SessionInfo, error) {
	result, err := CallFirst[domain.ResumeHandler](f.registry, func(h domain.ResumeHandler) (any, error) {
		return h.ResolveResumeChoice(ctx, chat, sessionIndex)
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return result.(*domain.SessionInfo), nil
}

// RegisterPendingPermission stores a pending permission for later resolution.
func (f *Framework) RegisterPendingPermission(reqID string, chat domain.ChatRef) chan domain.PermissionResponse {
	ch := make(chan domain.PermissionResponse, 1)
	f.pendingPerms.Store(reqID, &permEntry{ch: ch, chat: chat})
	return ch
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/framework/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/framework/framework.go internal/framework/framework_test.go
git commit -m "feat(framework): add Framework with ProcessInbound pipeline"
```

### Task 8: Verify full project builds and tests pass

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: success

- [ ] **Step 2: Run all tests**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 3: Final commit for Phase 1**

```bash
git add -A
git commit -m "milestone: Phase 1 skeleton complete — framework, hooks, registry"
```

---
