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

## Chunk 2: Phase 2 — Channel Layer Migration

Connects the Framework to the Telegram channel. After this chunk, the app boots via `Framework.Register()` + `Framework.Start()`.

### Task 1: Move Telegram channel to builtin/channel/telegram/

**Files:**
- Create: `internal/builtin/channel/telegram/` (copy from `internal/channel/telegram/`)
- Create: `internal/builtin/plugin.go`

- [ ] **Step 1: Create builtin directory structure**

```bash
mkdir -p internal/builtin/channel/telegram
```

- [ ] **Step 2: Copy Telegram channel files**

Copy all files from `internal/channel/telegram/` to `internal/builtin/channel/telegram/`. Update the package declaration to `package telegram` (should already match). Update import paths if needed.

- [ ] **Step 3: Move AllowlistChecker to builtin/channel/telegram/**

Copy `internal/dispatcher/allowlist.go` to `internal/builtin/channel/telegram/allowlist.go`. Update package to `telegram`. Update imports.

- [ ] **Step 4: Verify compilation**

Run: `go build ./internal/builtin/...`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add internal/builtin/
git commit -m "feat(builtin): copy telegram channel and allowlist to builtin package"
```

### Task 2: Update Telegram channel to accept Framework callbacks

**Files:**
- Modify: `internal/builtin/channel/telegram/channel.go`

- [ ] **Step 1: Replace CallbackHandlers with Framework reference**

Change `TelegramChannel` to accept a callback interface instead of direct dispatcher references:

```go
type FrameworkCallbacks interface {
	RespondPermission(reqID string, decision domain.PermissionDecision)
	HandleBusySendNow(chat domain.ChatRef, token string) (bool, error)
	ResolveResumeChoice(ctx context.Context, chat domain.ChatRef, sessionIndex int) (*domain.SessionInfo, error)
}
```

Update `NewTelegramChannel` to accept `FrameworkCallbacks` instead of `CallbackHandlers`. In the callback handler methods, construct `ChatRef` from the Telegram chat ID and call Framework methods. The channel maps callback data strings (`"always"`, `"once"`, `"deny"`) to `domain.PermissionDecision` before calling.

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/builtin/...`

- [ ] **Step 3: Run tests**

Run: `go test ./internal/builtin/... -v`

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(builtin/telegram): use FrameworkCallbacks instead of dispatcher"
```

### Task 3: Create BuiltinPlugin

**Files:**
- Create: `internal/builtin/plugin.go`

- [ ] **Step 1: Write BuiltinPlugin struct**

```go
package builtin

import (
	"github.com/zhu327/acpclaw/internal/builtin/channel/telegram"
	"github.com/zhu327/acpclaw/internal/config"
	"github.com/zhu327/acpclaw/internal/domain"
)

type BuiltinPlugin struct {
	cfg        *config.Config
	echo       bool
	fw         any // *framework.Framework, set during Init
	tgChannel  *telegram.TelegramChannel
	sessionMgr domain.SessionManager
	prompter   domain.Prompter
}

func NewPlugin(cfg *config.Config, echoMode bool) (*BuiltinPlugin, error) {
	return &BuiltinPlugin{cfg: cfg, echo: echoMode}, nil
}

func (b *BuiltinPlugin) Name() string { return "builtin" }

func (b *BuiltinPlugin) Init(fw any) error {
	b.fw = fw
	b.buildAgentService()
	b.buildTelegramChannel()
	return nil
}

func (b *BuiltinPlugin) Channels() []domain.Channel {
	if b.tgChannel != nil {
		return []domain.Channel{b.tgChannel}
	}
	return nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/builtin/...`

- [ ] **Step 3: Commit**

```bash
git add internal/builtin/plugin.go
git commit -m "feat(builtin): add BuiltinPlugin skeleton"
```

### Task 4: Wire Framework in main.go

**Files:**
- Modify: `cmd/acpclaw/main.go`
- Modify: `cmd/acpclaw/app.go`

- [ ] **Step 1: Update run() to use Framework**

Replace `SetupApp` in `cmd/acpclaw/main.go`. The `BuiltinPlugin.Init()` method now builds all components that `SetupApp` previously built:

- `buildAgentService()` creates `AcpAgentService` (or echo service), stores as `b.sessionMgr` and `b.prompter` (the concrete type implements both interfaces)
- `buildTelegramChannel()` creates the Telegram bot, channel, and stores as `b.tgChannel`
- Memory service and cron scheduler are initialized if enabled

`run()` becomes:

```go
fw := framework.New()
bp, err := builtin.NewPlugin(cfg, *echoMode)
fw.Register(bp)
fw.Init()
ctx, stop := signal.NotifyContext(...)
defer stop()
return fw.Start(ctx)
```

Delete `SetupApp` and the `App` struct from `app.go`. Move the `buildXxx` helper functions into `BuiltinPlugin.Init()` as private methods.

In Phase 2, `ProcessInbound` is connected but the full pipeline is not yet wired. Messages flow through the Framework but the `ActionExecutor` hook (BuiltinPlugin) delegates to the agent service's Prompt method directly. Command handling is added in Phase 3.

- [ ] **Step 2: Verify compilation and manual test**

Run: `go build ./cmd/acpclaw/`
Manual test: run in echo mode, send a message, verify response.

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "milestone: Phase 2 — channel layer migrated to Framework"
```

---

## Chunk 3: Phase 3 — Command System Migration

Extracts commands from dispatcher into standalone structs.

### Task 1: Create builtin/commands/ — One file per command

**Files:**
- Create: `internal/builtin/commands/new.go`
- Create: `internal/builtin/commands/reconnect.go`
- Create: `internal/builtin/commands/cancel.go`
- Create: `internal/builtin/commands/status.go`
- Create: `internal/builtin/commands/session.go`
- Create: `internal/builtin/commands/resume.go`
- Create: `internal/builtin/commands/help.go`
- Create: `internal/builtin/commands/start.go`
- Create: `internal/builtin/commands/util.go`

- [ ] **Step 1: Create shared utilities**

Create `internal/builtin/commands/util.go` with helper functions extracted from `dispatcher/commands.go`:

```go
package commands

import (
	"log/slog"
	"github.com/zhu327/acpclaw/internal/domain"
)

func replyText(resp domain.Replier, text string) {
	if err := resp.Reply(domain.OutboundMessage{Text: text}); err != nil {
		slog.Debug("reply failed (best effort)", "error", err)
	}
}

func resolveWorkspace(args []string, defaultWs string) string {
	ws := strings.TrimSpace(strings.Join(args, " "))
	if ws == "" {
		if defaultWs != "" {
			return defaultWs
		}
		return "."
	}
	return ws
}

func convertToPromptInput(msg domain.InboundMessage) domain.PromptInput {
	// Migrated from dispatcher/dispatcher.go convertToPromptInput
	input := domain.PromptInput{Text: msg.Text}
	for _, att := range msg.Attachments {
		switch att.MediaType {
		case "image":
			input.Images = append(input.Images, domain.ImageData{
				MIMEType: "image/jpeg", Data: att.Data, Name: att.FileName,
			})
		default:
			fd := domain.FileData{MIMEType: att.MediaType, Data: att.Data, Name: att.FileName}
			if unicode.utf8.Valid(att.Data) {
				s := string(att.Data)
				fd.TextContent = &s
			}
			if fd.MIMEType == "" { fd.MIMEType = "application/octet-stream" }
			if fd.Name == "" { fd.Name = "attachment.bin" }
			input.Files = append(input.Files, fd)
		}
	}
	return input
}
```

- [ ] **Step 2: Create each command struct**

Each command file follows this pattern (example for `/new`):

```go
package commands

import (
	"context"
	"fmt"
	"github.com/zhu327/acpclaw/internal/domain"
)

type NewCommand struct {
	sessionMgr  domain.SessionManager
	defaultWs   string
}

func NewNewCommand(sm domain.SessionManager, defaultWs string) *NewCommand {
	return &NewCommand{sessionMgr: sm, defaultWs: defaultWs}
}

func (c *NewCommand) Name() string        { return "new" }
func (c *NewCommand) Description() string { return "Start a new session" }

func (c *NewCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	// Migrate logic from dispatcher.handleNew
	workspace := resolveWorkspace(args, c.defaultWs)
	if err := c.sessionMgr.NewSession(ctx, tc.Chat, workspace); err != nil {
		return &domain.Result{Text: "❌ Failed to start session."}, nil
	}
	info := c.sessionMgr.ActiveSession(tc.Chat)
	if info != nil {
		return &domain.Result{Text: fmt.Sprintf("Session started: `%s` in `%s`", info.SessionID, info.Workspace)}, nil
	}
	return &domain.Result{Text: "Session started."}, nil
}
```

Create similar files for each command, migrating logic from `dispatcher/commands.go`:
- `help.go` — reads commands from `tc.State["commands"]`, generates dynamic help text
- `start.go` — returns welcome message
- `cancel.go` — calls `Prompter.Cancel()`
- `status.go` — calls `SessionManager.ActiveSession()`
- `session.go` — calls `SessionManager.ListSessions()`
- `resume.go` — calls `SessionManager.ListSessions()` + `LoadSession()`
- `reconnect.go` — calls `SessionManager.Reconnect()`

- [ ] **Step 3: Write tests for each command**

Create `internal/builtin/commands/new_test.go`, etc. with mock SessionManager/Prompter.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/builtin/commands/... -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/builtin/commands/
git commit -m "feat(builtin/commands): extract all commands from dispatcher"
```

### Task 2: Implement CommandProvider and MessageRouter in BuiltinPlugin

**Files:**
- Modify: `internal/builtin/plugin.go`

- [ ] **Step 1: Add CommandProvider implementation**

```go
func (b *BuiltinPlugin) Commands() []domain.Command {
	return []domain.Command{
		commands.NewStartCommand(),
		commands.NewHelpCommand(),
		commands.NewNewCommand(b.sessionMgr, b.cfg.Agent.Workspace),
		commands.NewSessionCommand(b.sessionMgr),
		commands.NewResumeCommand(b.sessionMgr),
		commands.NewCancelCommand(b.prompter),
		commands.NewReconnectCommand(b.sessionMgr, b.cfg.Agent.Workspace),
		commands.NewStatusCommand(b.sessionMgr),
	}
}
```

- [ ] **Step 2: Add MessageRouter implementation**

```go
func (b *BuiltinPlugin) RouteMessage(ctx context.Context, msg domain.InboundMessage, state domain.State) (domain.Action, error) {
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text[1:])
		name := strings.ToLower(parts[0])
		var args []string
		if len(parts) > 1 {
			args = parts[1:]
		}
		return domain.Action{Kind: domain.ActionCommand, Command: name, Args: args}, nil
	}
	return domain.Action{
		Kind:  domain.ActionPrompt,
		Input: convertToPromptInput(msg),
	}, nil
}
```

- [ ] **Step 3: Verify compilation and tests**

Run: `go build ./... && go test ./... -v`

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "milestone: Phase 3 — command system migrated to plugin"
```

---

## Chunk 4: Phase 4 — Turn Lifecycle Migration

The core migration: removes the dispatcher, implements the full pipeline.

### Task 1: Implement SessionResolver in BuiltinPlugin

**Files:**
- Modify: `internal/builtin/plugin.go`

- [ ] **Step 1: Add ResolveSession**

Migrate the implicit session creation logic from `dispatcher.go Handle()`:

```go
func (b *BuiltinPlugin) ResolveSession(ctx context.Context, msg domain.InboundMessage) (string, error) {
	info := b.sessionMgr.ActiveSession(msg.ChatRef)
	if info != nil {
		return info.SessionID, nil
	}
	workspace := b.cfg.Agent.Workspace
	if workspace == "" {
		workspace = "."
	}
	if err := b.sessionMgr.NewSession(ctx, msg.ChatRef, workspace); err != nil {
		return "", err
	}
	info = b.sessionMgr.ActiveSession(msg.ChatRef)
	if info != nil {
		return info.SessionID, nil
	}
	return "", nil
}
```

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(builtin): implement SessionResolver hook"
```

### Task 2: Implement ActionExecutor with busy queue

**Files:**
- Modify: `internal/builtin/plugin.go` (or create `internal/builtin/executor.go`)

- [ ] **Step 1: Migrate busy queue logic**

Move `convMu`, `pendingByChat`, `queueBusyPrompt`, `popPending`, `runPromptLoop` from dispatcher to a new `Executor` struct in `internal/builtin/executor.go`. This struct implements `domain.ActionExecutor` and `domain.BusyHandler`.

- [ ] **Step 2: Write tests**

Test busy queue: when executor is busy, second message is queued. HandleBusySendNow cancels and processes queued message.

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(builtin): implement ActionExecutor with busy queue"
```

### Task 3: Implement OutboundRenderer and OutboundDispatcher

**Files:**
- Modify: `internal/builtin/plugin.go`

- [ ] **Step 1: Add render/dispatch implementations**

```go
func (b *BuiltinPlugin) RenderOutbound(ctx context.Context, result *domain.Result, state domain.State) ([]domain.OutboundMessage, error) {
	if result.Reply == nil {
		return nil, nil
	}
	return []domain.OutboundMessage{{
		Text:   result.Reply.Text,
		Images: result.Reply.Images,
		Files:  result.Reply.Files,
	}}, nil
}

func (b *BuiltinPlugin) DispatchOutbound(ctx context.Context, msg domain.OutboundMessage, resp domain.Responder) error {
	return resp.Reply(msg)
}
```

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(builtin): implement OutboundRenderer and OutboundDispatcher"
```

### Task 4: Implement ErrorObserver

- [ ] **Step 1: Add error handler**

```go
func (b *BuiltinPlugin) OnError(ctx context.Context, stage string, err error, msg domain.InboundMessage) {
	slog.Error("turn error", "stage", stage, "chat", msg.ChatRef.CompositeKey(), "error", err)
}
```

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(builtin): implement ErrorObserver"
```

### Task 5: Switch ChatID to raw values

**Files:**
- Modify: `internal/builtin/channel/telegram/channel.go`
- Modify: `cmd/acpclaw/app.go` (cron)
- Modify: `internal/agent/service.go` (still in old location until Phase 6)

- [ ] **Step 1: Update Telegram channel to set raw ChatID**

Change `ChatRef.ChatID` from `"telegram:12345"` to `"12345"`. Update all internal maps in agent service to use `chat.CompositeKey()`.

- [ ] **Step 2: Update agent service to use ChatRef**

Replace all `chatID string` parameters with `chat domain.ChatRef`. Use `chat.CompositeKey()` for internal map keys (`liveByChat`, `sessionHistory`, `promptLocks`, `sessionLocks`).

- [ ] **Step 3: Run all tests, fix failures**

Run: `go test ./... -v`

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "refactor: switch to raw ChatID, use CompositeKey() for map keys"
```

### Task 6: Delete dispatcher package

- [ ] **Step 1: Remove dispatcher**

Delete `internal/dispatcher/` directory. Remove all imports of `dispatcher` package from `cmd/acpclaw/`.

- [ ] **Step 2: Verify compilation and tests**

Run: `go build ./... && go test ./...`

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "milestone: Phase 4 — dispatcher eliminated, full turn lifecycle active"
```

---

## Chunk 5: Phase 5 — Memory and Cron Migration

### Task 1: Memory as ContextLoader and StateSaver

**Files:**
- Move: `internal/memory/` → `internal/builtin/memory/`
- Modify: `internal/builtin/plugin.go`

- [ ] **Step 1: Move memory package**

```bash
mv internal/memory internal/builtin/memory
```

Update all import paths.

- [ ] **Step 2: Implement ContextLoader**

In BuiltinPlugin, implement `LoadContext` to inject memory context:

```go
func (b *BuiltinPlugin) LoadContext(ctx context.Context, sessionID string, state domain.State) error {
	if b.memorySvc == nil || !b.cfg.Memory.FirstPromptContext {
		return nil
	}
	memCtx, err := b.memorySvc.BuildSessionContext(ctx)
	if err != nil {
		return nil // non-fatal
	}
	state["memory_context"] = memCtx
	return nil
}
```

- [ ] **Step 3: Implement StateSaver**

`SaveState` is called after each turn. It handles history persistence and auto-summarize. The turn's user input and agent reply are passed via `state["user_text"]` and `state["reply"]` (set by ActionExecutor before returning).

```go
func (b *BuiltinPlugin) SaveState(ctx context.Context, sessionID string, state domain.State) error {
	if b.memorySvc == nil {
		return nil
	}
	if userText, ok := state["user_text"].(string); ok && userText != "" {
		_ = b.memorySvc.AppendHistory(sessionID, "user", userText)
	}
	if reply, ok := state["reply"].(*domain.AgentReply); ok && reply != nil && reply.Text != "" {
		_ = b.memorySvc.AppendHistory(sessionID, "assistant", reply.Text)
	}
	return nil
}
```

Auto-summarize is triggered by commands (`/new`, `/reconnect`, `/resume`) via the command's `Execute` method, not by `SaveState`. This matches the current behavior where `summarizeIfEnabled` is called explicitly before session changes.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/builtin/... -v`

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(builtin): migrate memory as ContextLoader/StateSaver"
```

### Task 2: Cron migration

**Files:**
- Move: `internal/cron/` → `internal/builtin/cron/`
- Modify: `internal/builtin/plugin.go`

- [ ] **Step 1: Move cron package and update imports**

- [ ] **Step 2: Update cron trigger to use Framework.ProcessInbound**

The cron scheduler's `OnTrigger` constructs an `InboundMessage` with raw `ChatRef` and a `BackgroundResponder`, then calls `Framework.ProcessInbound()`. The BuiltinPlugin stores a responder factory (provided by the Telegram channel during `Init`):

```go
scheduler.OnTrigger(func(job domain.CronJob) {
	chatIDInt, _ := strconv.ParseInt(job.ChatID, 10, 64)
	resp := telegram.NewBackgroundResponder(b.tgBot, chatIDInt)
	msg := domain.InboundMessage{
		ChatRef: domain.ChatRef{ChannelKind: job.Channel, ChatID: job.ChatID},
		Text:    job.Message,
	}
	fw.ProcessInbound(ctx, msg, resp)
})
```

The `tgBot` reference is stored in BuiltinPlugin during `Init` alongside the channel construction.

- [ ] **Step 3: Run tests**

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "milestone: Phase 5 — memory and cron migrated to builtin"
```

---

## Chunk 6: Phase 6 — Cleanup

### Task 1: Move agent to builtin/agent/

**Files:**
- Move: `internal/agent/` → `internal/builtin/agent/`

- [ ] **Step 1: Move and update imports**

```bash
mv internal/agent internal/builtin/agent
```

Update all import paths. Update agent service to implement the new split interfaces (`SessionManager`, `Prompter`, `PermissionHandler`, `ActivityObserver`).

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "refactor: move agent to builtin/agent"
```

### Task 2: Move MCP to builtin/mcp/

**Files:**
- Move: `internal/mcp/` → `internal/builtin/mcp/`

- [ ] **Step 1: Move and update imports**

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "refactor: move mcp to builtin/mcp"
```

### Task 3: Simplify main.go

**Files:**
- Modify: `cmd/acpclaw/main.go`

- [ ] **Step 1: Slim down main.go**

Remove all `buildXxx` helper functions from `app.go`. The `run()` function becomes:

```go
func run() error {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	configPath := flag.String("config", "config.yaml", "Path to YAML config file")
	echoMode := flag.Bool("echo", false, "Use echo mode for testing")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	initLogging(cfg)

	fw := framework.New()
	bp, err := builtin.NewPlugin(cfg, *echoMode)
	if err != nil {
		return err
	}
	fw.Register(bp)
	if err := fw.Init(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return fw.Start(ctx)
}
```

- [ ] **Step 2: Delete app.go**

Remove `cmd/acpclaw/app.go` entirely.

- [ ] **Step 3: Verify compilation and tests**

Run: `go build ./... && go test ./...`

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "refactor: simplify main.go, delete app.go"
```

### Task 4: Delete old packages

- [ ] **Step 1: Remove old package directories**

```bash
rm -rf internal/agent internal/channel internal/dispatcher internal/memory internal/cron internal/mcp
```

- [ ] **Step 2: Remove old AgentService interface**

Remove the old `AgentService` interface from `domain/agent.go`, keeping only the split interfaces.

- [ ] **Step 3: Clean up domain/channel.go**

Remove `MessageHandler` (old signature), `AllowlistChecker` (moved to telegram package), and `Channel.Send` method. Update `Channel` interface to use `ctx` parameter in `Start`.

- [ ] **Step 4: Final verification**

Run: `go build ./... && go test ./...`

- [ ] **Step 5: Manual test**

Run in echo mode. Test: send message, /new, /help, /cancel, /status, /session.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "milestone: Phase 6 — cleanup complete, architecture refactoring done"
```

---
