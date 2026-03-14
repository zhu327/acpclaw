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

// PluginContext is the interface the framework exposes to plugins during Init.
// Defined here (not in the framework package) to avoid circular imports.
type PluginContext interface {
	GetResponder(chat ChatRef) Responder
	RegisterPendingPermission(reqID string, chat ChatRef) chan PermissionResponse
}

// PluginInitializer is implemented by plugins that need a reference to the
// framework at startup.
type PluginInitializer interface {
	Init(fw PluginContext) error
}
