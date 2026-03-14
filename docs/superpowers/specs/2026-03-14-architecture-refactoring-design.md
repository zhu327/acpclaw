# Acpclaw Architecture Refactoring Design

Date: 2026-03-14

Comprehensive architecture refactoring inspired by the bub agent framework. Introduces a plugin/hook system, turn lifecycle pipeline, and clean separation of concerns via Interface + Registry pattern.

## Motivation

Acpclaw grew organically without upfront architecture. Key problems:

- `app.go` is a god object that wires everything with Telegram-specific knowledge
- `Dispatcher` is tightly coupled to Telegram callbacks (`int64` chatID in `HandleBusySendNow`, `ResolveResumeChoice`)
- `AgentService` interface is too large (session management, prompting, cancellation, permissions, activity callbacks)
- Cron is hardcoded to Telegram channel
- No clear turn lifecycle — message processing logic is scattered across dispatcher and agent service
- Adding a new channel or extending behavior requires modifying core code

The bub project demonstrates a clean plugin-based architecture with formal hook contracts, a well-defined turn lifecycle, and clear module boundaries. This design adapts those patterns to Go using idiomatic Interface + Registry.

## Architecture Overview

### Package Structure

```
acpclaw/
├── cmd/acpclaw/
│   └── main.go                    # Entry point: create Framework, register builtin, start
├── internal/
│   ├── framework/
│   │   ├── framework.go           # Framework: ProcessInbound, lifecycle management
│   │   ├── registry.go            # HookRegistry: plugin registration, CallFirst/CallAll
│   │   └── types.go               # TurnContext, State, Action, Result
│   ├── domain/
│   │   ├── hooks.go               # All hook interface definitions
│   │   ├── channel.go             # Channel, Responder, InboundMessage, OutboundMessage
│   │   ├── command.go             # Command interface
│   │   ├── types.go               # ImageData, FileData, Attachment
│   │   ├── agent.go               # SessionManager, Prompter (split interfaces)
│   │   ├── memory.go              # MemoryEntry
│   │   ├── permission.go          # Permission types
│   │   ├── activity.go            # ActivityBlock
│   │   ├── cron.go                # CronJob
│   │   └── errors.go              # Error definitions
│   ├── builtin/
│   │   ├── plugin.go              # BuiltinPlugin: implements all hook interfaces
│   │   ├── agent/                 # Agent implementation (migrated from internal/agent)
│   │   │   ├── service.go
│   │   │   ├── process.go
│   │   │   ├── echo.go
│   │   │   └── summarizer.go
│   │   ├── channel/
│   │   │   └── telegram/          # Telegram channel (migrated from internal/channel/telegram)
│   │   ├── memory/                # Memory service (migrated from internal/memory)
│   │   ├── cron/                  # Cron scheduler (migrated from internal/cron)
│   │   ├── commands/              # Built-in commands (/new, /reconnect, etc.)
│   │   └── mcp/                   # MCP server (migrated from internal/mcp)
│   ├── config/
│   └── templates/
```

### Layering

```
cmd/acpclaw/main.go
    └── Framework (orchestration)
            ├── HookRegistry (plugin registration and invocation)
            └── Plugins
                  └── BuiltinPlugin (default implementation)
                        ├── Agent (SessionManager + Prompter)
                        ├── Telegram Channel
                        ├── Memory
                        ├── Cron
                        ├── Commands (/new, /help, ...)
                        └── MCP
```

### Dependency Rules

- `domain/` is pure types and interfaces, zero external dependencies; all packages may import it
- `framework/` depends only on `domain/`; knows nothing about concrete implementations
- `builtin/` implements `domain/` hook interfaces, registers into `framework/`
- `cmd/` assembles: creates Framework → registers BuiltinPlugin → starts

## Hook Interfaces

All hook interfaces defined in `domain/hooks.go`. Framework uses type assertion to detect which interfaces a plugin implements.

### Turn Lifecycle Hooks

```go
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
// CallAll semantics: all implementations called (in finally/defer block).
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
```

### Provider Hooks

```go
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
```

### Observer Hooks

```go
// ErrorObserver is notified when errors occur during turn processing.
// CallAll semantics, fault-isolated (one failure doesn't block others).
type ErrorObserver interface {
    OnError(ctx context.Context, stage string, err error, msg InboundMessage)
}
```

## Turn Types

Defined in `framework/types.go`:

```go
type State map[string]any

type ActionKind int
const (
    ActionCommand ActionKind = iota
    ActionPrompt
)

type Action struct {
    Kind    ActionKind
    Command string
    Args    []string
    Input   domain.PromptInput
}

type Result struct {
    Reply            *domain.AgentReply
    Text             string
    SuppressOutbound bool
}

type TurnContext struct {
    SessionID string
    Message   domain.InboundMessage
    Responder domain.Responder
    State     State
}
```

## HookRegistry

```go
type Plugin interface {
    Name() string
}

type HookRegistry struct {
    plugins []Plugin // registration order; later = higher priority
}

func (r *HookRegistry) Register(p Plugin)

// CallFirst calls implementations of hook interface T in reverse registration order,
// returns the first non-zero result.
func CallFirst[T any](r *HookRegistry, fn func(T) (any, error)) (any, error)

// CallAll calls all implementations of hook interface T, collects errors.
func CallAll[T any](r *HookRegistry, fn func(T) error) []error
```

Internally iterates `plugins`, performs type assertion (`if h, ok := p.(T); ok`), and calls in priority order.

## Framework

```go
type Framework struct {
    registry *HookRegistry
    channels map[string]domain.Channel
    commands map[string]domain.Command
}

func New() *Framework
func (f *Framework) Register(p Plugin)
func (f *Framework) Init() error    // collects channels and commands from providers
func (f *Framework) Start(ctx context.Context) error  // starts channels, blocks

// Callback entry points for channels
func (f *Framework) RespondPermission(reqID string, decision domain.PermissionDecision)
func (f *Framework) HandleBusySendNow(chatID string)
func (f *Framework) ResolveResumeChoice(chatID string, sessionIndex int)
```

### ProcessInbound Pipeline

```go
func (f *Framework) ProcessInbound(ctx context.Context, msg domain.InboundMessage, resp domain.Responder) error {
    // 1. ResolveSession — CallFirst[SessionResolver]
    // 2. LoadContext — CallAll[ContextLoader] with shared state
    // 3. RouteMessage — CallFirst[MessageRouter]
    // 4. ExecuteAction:
    //    - ActionCommand: lookup in f.commands, call cmd.Execute()
    //    - ActionPrompt: CallFirst[ActionExecutor]
    // 5. SaveState — defer CallAll[StateSaver]
    // 6. RenderOutbound — CallAll[OutboundRenderer] (skip if SuppressOutbound)
    // 7. DispatchOutbound — CallAll[OutboundDispatcher] for each outbound
    //
    // Error handling: catch top-level exceptions, notify CallAll[ErrorObserver], re-raise
}
```

### Bootstrap

```go
// cmd/acpclaw/main.go
func run(configPath string, echoMode bool) error {
    cfg, _ := config.Load(configPath)
    fw := framework.New()
    bp, _ := builtin.NewPlugin(cfg, echoMode)
    fw.Register(bp)
    fw.Init()
    ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    return fw.Start(ctx)
}
```

## Channel Abstraction

```go
type Channel interface {
    Kind() string
    Start(ctx context.Context, handler MessageHandler) error
    Stop() error
}

type MessageHandler func(ctx context.Context, msg InboundMessage, resp Responder)
```

Changes from current design:
- `Start` takes `ctx` for shutdown signaling
- `Send(chatID, msg)` removed — sending handled by `Responder` and `OutboundDispatcher` hook

### Responder

```go
type Responder interface {
    Replier
    ChannelKind() string
    ShowPermissionUI(req ChannelPermissionRequest) error
    ShowTypingIndicator() error
    SendActivity(block ActivityBlock) error
    ShowBusyNotification(token string, replyToMsgID int) (notifyMsgID int, err error)
    ClearBusyNotification(notifyMsgID int) error
    ShowResumeKeyboard(sessions []SessionChoice) error
}
```

`ChannelKind()` added so framework can identify the source channel without parsing composite keys.

### Composite Key

Channels set composite key when constructing `InboundMessage`:

```go
msg := domain.InboundMessage{
    ChatID:      "telegram:" + strconv.FormatInt(tgMsg.Chat.ID, 10),
    ChannelKind: "telegram",
}
```

Framework and all hooks treat `ChatID` as an opaque string.

### Callback Decoupling

Channels route callbacks through Framework methods instead of directly calling Dispatcher:

- Permission: Channel → `Framework.RespondPermission(reqID, decision)`
- Busy: Channel → `Framework.HandleBusySendNow(chatID)`
- Resume: Channel → `Framework.ResolveResumeChoice(chatID, sessionIndex)`

## Command System

```go
type Command interface {
    Name() string
    Description() string
    Execute(ctx context.Context, args []string, tc *TurnContext) (*Result, error)
}
```

Built-in commands registered via `CommandProvider`:

| Command | Dependencies |
|---------|-------------|
| `/new` | `SessionManager` |
| `/reconnect` | `SessionManager` |
| `/cancel` | `Prompter` |
| `/status` | `SessionManager` |
| `/session` | `SessionManager` |
| `/resume` | `SessionManager` |
| `/help` | None (reads registered commands from state) |
| `/start` | None |

Each command is a standalone struct depending only on the minimal interface it needs. Commands are in `builtin/commands/`, one file per command.

Memory and Cron are managed via MCP tools, not user-facing commands.

## BuiltinPlugin Hook Mapping

| Hook Interface | BuiltinPlugin Implementation | Migrated From |
|---|---|---|
| `SessionResolver` | Parse composite key, find active session | `dispatcher.go` session lookup |
| `ContextLoader` | Inject memory context into state (first prompt context) | `dispatcher.go` firstPromptContext logic |
| `MessageRouter` | Parse `/command` prefix, split command vs prompt | `dispatcher/commands.go` command parsing |
| `ActionExecutor` | Call `Prompter.Prompt()`, manage busy state | `dispatcher.go` handlePrompt |
| `StateSaver` | History persistence, auto summarize | `dispatcher.go` summarize logic |
| `OutboundRenderer` | Convert `AgentReply` to `OutboundMessage` | `dispatcher.go` reply formatting |
| `OutboundDispatcher` | Send via `Responder.Reply()` | `dispatcher.go` send logic |
| `ChannelProvider` | Return `[TelegramChannel]` | `app.go` channel construction |
| `CommandProvider` | Return built-in command list | `dispatcher/commands.go` |
| `ErrorObserver` | Log error + notify user via responder | Scattered error handling |

### Agent Interface Split

Current `AgentService` splits into:

| Interface | Methods |
|---|---|
| `SessionManager` | `NewSession`, `LoadSession`, `ListSessions`, `ActiveSession`, `Reconnect`, `Shutdown` |
| `Prompter` | `Prompt`, `Cancel` |
| `PermissionHandler` | `SetPermissionHandler`, `SetSessionPermissionMode` |
| `ActivityObserver` | `SetActivityHandler` |

The concrete `AgentServiceImpl` in `builtin/agent/` implements all four interfaces. Consumers receive only the interface they need.

### Dispatcher Elimination

The current `dispatcher` package is fully decomposed:

- Command parsing → `MessageRouter` hook
- Command execution → `Command` interface + individual command structs
- Prompt handling / busy management → `ActionExecutor` hook
- Permission & allowlist → Channel layer
- Session info construction → `ContextLoader` hook

The `dispatcher` package is deleted at the end of migration.

## Incremental Migration Plan

Six phases; the project compiles and runs after each phase.

### Phase 1: Skeleton

- Create `internal/framework/`: `Framework`, `HookRegistry`, turn types
- Add `domain/hooks.go` (all hook interfaces), `domain/command.go` (Command interface)
- Split `domain/agent.go`: `AgentService` → `SessionManager` + `Prompter` + `PermissionHandler` + `ActivityObserver`
- No changes to existing runtime; new code only

### Phase 2: Channel Layer

- Update `Channel` interface (add `ctx` param, remove `Send`)
- Telegram channel implements `Start(ctx, handler)`
- Callbacks route through `Framework` methods instead of Dispatcher
- Create `builtin/plugin.go`, implement `ChannelProvider`
- `main.go` starts via `Framework.Register(builtinPlugin)` + `Framework.Start()`

### Phase 3: Command System

- Create `builtin/commands/`, one file per command
- Extract each `handleXxx` from `dispatcher/commands.go` into standalone `Command` struct
- BuiltinPlugin implements `CommandProvider` + `MessageRouter`
- Framework `ProcessInbound` implements command routing
- Delete `dispatcher/commands.go`

### Phase 4: Turn Lifecycle

- BuiltinPlugin implements `SessionResolver`, `ActionExecutor`, `OutboundRenderer`, `OutboundDispatcher`
- Migrate prompt handling, busy management, reply sending from `dispatcher.go` to hooks
- Framework `ProcessInbound` fully implements 7-step pipeline
- Delete `dispatcher/` package

### Phase 5: Memory / Cron

- Memory: BuiltinPlugin implements `ContextLoader` (inject memory context) and `StateSaver` (history + auto summarize)
- Cron: migrate to `builtin/cron/`, triggers go through `Framework.ProcessInbound()`
- Move `internal/memory/` → `builtin/memory/`, `internal/cron/` → `builtin/cron/`

### Phase 6: Cleanup

- Simplify `main.go` to ~15 lines
- Delete old packages: `internal/agent/`, `internal/channel/`, `internal/dispatcher/`, `internal/memory/`, `internal/cron/`
- Migrate agent implementation to `builtin/agent/`
- Update all tests

### Phase Validation

Each phase must satisfy:
- Compiles successfully
- Existing tests pass
- Manual test (echo mode): send message, run commands, permission flow

## Testing Strategy

- **Framework tests**: mock plugins implementing specific hook interfaces, verify ProcessInbound pipeline ordering and CallFirst/CallAll semantics
- **Registry tests**: verify type assertion, priority ordering, CallFirst returns first non-zero, CallAll collects all
- **Command tests**: each command struct tested in isolation with mock SessionManager/Prompter
- **Integration**: echo mode end-to-end with mock channel
- **Existing tests**: migrated alongside their code, updated for new package paths
