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
			handler := func(msgCtx context.Context, msg domain.InboundMessage, resp domain.Responder) {
				if err := f.ProcessInbound(msgCtx, msg, resp); err != nil {
					slog.Error("ProcessInbound failed", "chat", msg.CompositeKey(), "error", err)
				}
			}
			return ch.Start(gCtx, handler)
		})
	}
	return g.Wait()
}

// ProcessInbound executes the 7-step turn lifecycle pipeline.
func (f *Framework) ProcessInbound(
	ctx context.Context,
	msg domain.InboundMessage,
	resp domain.Responder,
) (retErr error) {
	key := msg.CompositeKey()
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
	state["chat_id"] = key
	state["session_id"] = sessionID
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

	action := defaultAction(msg, actionResult)

	tc := &domain.TurnContext{
		Chat:      msg.ChatRef,
		SessionID: sessionID,
		Message:   msg,
		Responder: resp,
		State:     state,
	}

	// 4. SaveState (deferred, always runs after execute)
	defer func() {
		saveErrs := CallAll[domain.StateSaver](f.registry, func(h domain.StateSaver) error {
			return h.SaveState(ctx, sessionID, state)
		})
		if len(saveErrs) > 0 {
			slog.Warn("state saver errors", "errors", saveErrs)
		}
	}()

	// 5. ExecuteAction
	result, err := f.executeAction(ctx, action, tc)
	if err != nil {
		return err
	}
	if result == nil || result.SuppressOutbound {
		return nil
	}

	// 6. RenderOutbound + 7. DispatchOutbound
	f.renderAndDispatch(ctx, result, state, resp)
	return nil
}

func (f *Framework) executeAction(
	ctx context.Context,
	action domain.Action,
	tc *domain.TurnContext,
) (*domain.Result, error) {
	if action.Kind == domain.ActionCommand {
		cmd, ok := f.commands[action.Command]
		if !ok {
			return &domain.Result{Text: "Unknown command: /" + action.Command}, nil
		}
		result, err := cmd.Execute(ctx, action.Args, tc)
		if err != nil {
			return nil, fmt.Errorf("execute command %s: %w", action.Command, err)
		}
		return result, nil
	}
	execResult, err := CallFirst[domain.ActionExecutor](f.registry, func(h domain.ActionExecutor) (any, error) {
		return h.ExecuteAction(ctx, action, tc)
	})
	if err != nil {
		return nil, fmt.Errorf("execute action: %w", err)
	}
	if execResult == nil {
		return nil, nil
	}
	return execResult.(*domain.Result), nil
}

func defaultAction(msg domain.InboundMessage, actionResult any) domain.Action {
	if actionResult != nil {
		return actionResult.(domain.Action)
	}
	return domain.Action{
		Kind:  domain.ActionPrompt,
		Input: domain.PromptInput{Text: msg.Text},
	}
}

func (f *Framework) renderAndDispatch(
	ctx context.Context,
	result *domain.Result,
	state domain.State,
	resp domain.Responder,
) {
	var outbounds []domain.OutboundMessage
	CallAll[domain.OutboundRenderer](f.registry, func(h domain.OutboundRenderer) error {
		msgs, err := h.RenderOutbound(ctx, result, state)
		if err != nil {
			return err
		}
		outbounds = append(outbounds, msgs...)
		return nil
	})
	if len(outbounds) == 0 && result.Text != "" {
		outbounds = append(outbounds, domain.OutboundMessage{Text: result.Text})
	}
	for _, out := range outbounds {
		CallAll[domain.OutboundDispatcher](f.registry, func(h domain.OutboundDispatcher) error {
			return h.DispatchOutbound(ctx, out, resp)
		})
	}
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
		_, _ = CallFirst[domain.PermissionHandler](f.registry, func(h domain.PermissionHandler) (any, error) {
			h.SetSessionPermissionMode(pe.chat, domain.PermissionModeApprove)
			return nil, nil
		})
	}
}

// HandleBusySendNow delegates to the BusyHandler hook.
func (f *Framework) HandleBusySendNow(chat domain.ChatRef, token string) (bool, error) {
	result, err := CallFirst[domain.BusyHandler](f.registry, func(h domain.BusyHandler) (any, error) {
		ok, err := h.HandleBusySendNow(chat, token)
		if !ok || err != nil {
			return nil, err
		}
		return ok, nil
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
func (f *Framework) ResolveResumeChoice(
	ctx context.Context,
	chat domain.ChatRef,
	sessionIndex int,
) (*domain.SessionInfo, error) {
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
