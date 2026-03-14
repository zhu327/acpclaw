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

### Callback Hooks

```go
// BusyHandler handles the "send now" callback when a chat is busy.
// CallFirst semantics.
type BusyHandler interface {
    HandleBusySendNow(chatID string, token string) (ok bool, err error)
}

// ResumeHandler handles session resume choice callbacks.
// CallFirst semantics.
type ResumeHandler interface {
    ResolveResumeChoice(ctx context.Context, chatID string, sessionIndex int) (*SessionInfo, error)
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
type permEntry struct {
    ch     chan domain.PermissionResponse
    chatID string
}

type Framework struct {
    registry     *HookRegistry
    channels     map[string]domain.Channel
    commands     map[string]domain.Command
    responders   sync.Map // chatID → domain.Responder (active turn responders)
    pendingPerms sync.Map // reqID → permEntry (pending permission requests)
}

func New() *Framework
func (f *Framework) Register(p Plugin)
func (f *Framework) Init() error    // collects channels and commands from providers
func (f *Framework) Start(ctx context.Context) error  // starts channels, blocks

// GetResponder returns the active Responder for a chatID, or nil if no turn is active.
// Used by permission and activity handlers to send UI updates during a turn.
func (f *Framework) GetResponder(chatID string) domain.Responder

// Callback entry points for channels.
// Channels are responsible for converting channel-specific types (e.g. callback
// data strings "always"/"once"/"deny") to domain types before calling these methods.
func (f *Framework) RespondPermission(reqID string, decision domain.PermissionDecision)
func (f *Framework) HandleBusySendNow(chatID string, token string) (ok bool, err error)
func (f *Framework) ResolveResumeChoice(ctx context.Context, chatID string, sessionIndex int) (*domain.SessionInfo, error)
```

### ProcessInbound Pipeline

```go
func (f *Framework) ProcessInbound(ctx context.Context, msg domain.InboundMessage, resp domain.Responder) error {
    // Register responder for permission/activity handlers to find
    f.responders.Store(msg.ChatID, resp)
    defer f.responders.Delete(msg.ChatID)

    // 1. ResolveSession — CallFirst[SessionResolver]
    //    If all resolvers return empty, the builtin resolver auto-creates a new
    //    session via SessionManager.NewSession and returns the new session ID.

    // 2. LoadContext — CallAll[ContextLoader] with shared state
    //    State is created fresh (empty map) for each turn.
    //    Framework pre-populates state["commands"] with the registered command
    //    list before calling ContextLoader hooks.
    //    Each ContextLoader then mutates the shared state to inject its context
    //    (e.g. memory context, session info).

    // 3. RouteMessage — CallFirst[MessageRouter]

    // 4. ExecuteAction:
    //    - ActionCommand: lookup in f.commands, call cmd.Execute()
    //    - ActionPrompt: CallFirst[ActionExecutor]

    // 5. SaveState — defer CallAll[StateSaver] (always runs, even on error)

    // 6. RenderOutbound — CallAll[OutboundRenderer] (skip if SuppressOutbound)

    // 7. DispatchOutbound — CallAll[OutboundDispatcher] for each outbound

    // Error handling: on error at any stage, notify CallAll[ErrorObserver]
    // (fault-isolated: one observer failure doesn't block others), then return
    // the original error. ErrorObserver implementations can send error messages
    // to the user via the responder.
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

`ChannelKind()` remains on Responder for contexts where only the Responder is available (e.g. outbound dispatch, error observers). For inbound processing, `InboundMessage.ChannelKind` is the primary source.

### Channel Identification and Composite Key

`InboundMessage.ChannelKind` is the explicit channel identifier. `ChatID` stays as a pure, channel-local ID (e.g. `"12345"`, not `"telegram:12345"`):

```go
msg := domain.InboundMessage{
    ChatID:      strconv.FormatInt(tgMsg.Chat.ID, 10),
    ChannelKind: "telegram",
}
```

When a globally unique key is needed (map lookups, history paths, session resolution), the Framework constructs a composite key internally via a helper:

```go
func CompositeKey(channelKind, chatID string) string {
    return channelKind + ":" + chatID
}
```

Hooks and commands receive the `InboundMessage` with both fields available. They use `ChatID` for channel-local operations (e.g. Telegram API calls) and `ChannelKind` for channel-aware logic. The composite key is an internal concern of the Framework and storage layers — it is not exposed on `InboundMessage`.

### Callback Decoupling

Channels route callbacks through Framework methods instead of directly calling Dispatcher. The channel is responsible for converting channel-specific callback data to domain types before calling Framework methods.

- Permission: Channel receives callback string (`"always"`, `"once"`, `"deny"`) → maps to `domain.PermissionDecision` → calls `Framework.RespondPermission(reqID, decision)`
- Busy: Channel → `Framework.HandleBusySendNow(chatID, token)` → returns `(ok, error)` so channel can update its UI
- Resume: Channel → `Framework.ResolveResumeChoice(ctx, chatID, sessionIndex)` → returns `(*SessionInfo, error)` so channel can display the resumed session name

### Permission Flow

Permission requests flow through a pending request registry in the Framework:

1. During `ActionExecutor`, the agent's `PermissionHandler` is called with a permission request
2. The handler stores a `permEntry{ch, chatID}` in `Framework.pendingPerms` keyed by `reqID`, and sends permission UI to the user via `Framework.GetResponder(chatID).ShowPermissionUI()`
3. When the user responds, the channel maps the callback string to `domain.PermissionDecision` and calls `Framework.RespondPermission(reqID, decision)`
4. Framework looks up the `permEntry` by `reqID`, sends the decision on the channel, and removes the entry. If the decision is `PermissionAlways`, Framework also calls `PermissionHandler.SetSessionPermissionMode(chatID, PermissionModeApprove)` using the `chatID` from the stored entry.
5. Pending entries expire after 5 minutes (consistent with current behavior). A background goroutine or lazy cleanup on access removes stale entries.

### Plugin Initialization

Plugins that need a reference to the Framework receive it via an `Init` method. After `Register`, the Framework calls `Init(fw)` on any plugin that implements `PluginInitializer`:

```go
type PluginInitializer interface {
    Init(fw *Framework) error
}
```

BuiltinPlugin implements `PluginInitializer` to store the Framework reference, which it uses to access `GetResponder`, `pendingPerms`, and other Framework facilities when wiring permission and activity handlers.

### Activity and Responder Wiring

The Framework maintains a `responders` map (`sync.Map` of `chatID → Responder`). At the start of `ProcessInbound`, the current responder is stored; at the end it is removed.

Permission and activity handlers registered on the agent service use `Framework.GetResponder(chatID)` to find the active responder for sending permission UI and activity updates. If no turn is active (responder is nil), updates are dropped.

### Allowlist

Access control is handled at the channel layer. Each channel receives its allowlist configuration and checks incoming messages before invoking the Framework's `MessageHandler`. Messages from disallowed users are silently dropped, never reaching `ProcessInbound`. This keeps authorization logic channel-specific (Telegram uses user IDs and usernames; other channels may use different identity schemes).

The current `AllowlistChecker` and `DefaultAllowlistChecker` from `internal/dispatcher/` move to `builtin/channel/telegram/` during Phase 2, since they use Telegram-specific identity types (int64 user IDs, usernames).

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
| `/help` | None (reads registered command list from `TurnContext.State["commands"]`, injected by Framework during `LoadContext`) |
| `/start` | None |

Each command is a standalone struct depending only on the minimal interface it needs. Commands are in `builtin/commands/`, one file per command.

Memory and Cron are managed via MCP tools, not user-facing commands.

## BuiltinPlugin Hook Mapping

| Hook Interface | BuiltinPlugin Implementation | Migrated From |
|---|---|---|
| `SessionResolver` | Parse composite key, find active session | `dispatcher.go` session lookup |
| `ContextLoader` | Inject memory context into state (first prompt context) | `dispatcher.go` firstPromptContext logic |
| `MessageRouter` | Parse `/command` prefix, split command vs prompt | `dispatcher/commands.go` command parsing |
| `ActionExecutor` | Call `Prompter.Prompt()`, manage busy queue (see below) | `dispatcher.go` handlePrompt |
| `StateSaver` | History persistence, auto summarize | `dispatcher.go` summarize logic |
| `OutboundRenderer` | Convert `AgentReply` to `OutboundMessage` | `dispatcher.go` reply formatting |
| `OutboundDispatcher` | Send via `Responder.Reply()` | `dispatcher.go` send logic |
| `ChannelProvider` | Return `[TelegramChannel]` | `app.go` channel construction |
| `CommandProvider` | Return built-in command list | `dispatcher/commands.go` |
| `ErrorObserver` | Log error + notify user via responder | Scattered error handling |

### ActionExecutor Busy Queue

The BuiltinPlugin's `ActionExecutor` implementation migrates the current dispatcher's busy/queue behavior:

- Per-chat `sync.Mutex` (`TryLock`) to serialize prompt execution
- `pendingByChat` map: when a prompt arrives while another is in-flight, the new message is queued
- `ShowBusyNotification`: sent to the user with a "Send now" button when busy
- `HandleBusySendNow` flow: cancels the in-flight prompt, dequeues the pending message, and processes it
- `popPending`: retrieves and removes the queued message when the current prompt finishes

This state lives in the BuiltinPlugin (not in Framework), since it is implementation-specific behavior. The Framework's `HandleBusySendNow(chatID, token)` delegates to a `BusyHandler` interface that BuiltinPlugin implements.

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
- Cron: migrate to `builtin/cron/`, triggers go through `Framework.ProcessInbound()`. Cron constructs a `BackgroundResponder` (from the channel layer) for the target channel and passes it to `ProcessInbound` alongside the synthesized `InboundMessage`. The cron job's `Channel` field determines which channel's `BackgroundResponder` to use.
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
