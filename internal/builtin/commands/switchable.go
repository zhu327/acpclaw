package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhu327/acpclaw/internal/domain"
)

// ModelCommand handles /model [modelId].
type ModelCommand struct{ switchableCommand }

// ModeCommand handles /mode [modeId].
type ModeCommand struct{ switchableCommand }

// NewModelCommand creates a ModelCommand.
func NewModelCommand(mm domain.ModelManager) *ModelCommand {
	return &ModelCommand{switchableCommand{cfg: modelSwitchableConfig(mm)}}
}

// NewModeCommand creates a ModeCommand.
func NewModeCommand(mm domain.ModeManager) *ModeCommand {
	return &ModeCommand{switchableCommand{cfg: modeSwitchableConfig(mm)}}
}

func toSwitchableItems[T any](infos []T, get func(T) (id, name, desc string)) []switchableItem {
	items := make([]switchableItem, len(infos))
	for i, m := range infos {
		id, name, desc := get(m)
		items[i] = switchableItem{ID: id, Name: name, Description: desc}
	}
	return items
}

func modelSwitchableConfig(mm domain.ModelManager) switchableConfig {
	return switchableConfig{
		noun:            "model",
		errNotSupported: domain.ErrModelsNotSupported,
		errNotFound:     domain.ErrModelNotFound,
		getState: func(chat domain.ChatRef) (*switchableState, error) {
			ms, err := mm.GetModelState(chat)
			if err != nil {
				return nil, err
			}
			return &switchableState{
				CurrentID: ms.CurrentModelID,
				Available: toSwitchableItems(ms.Available, func(m domain.ModelInfo) (string, string, string) {
					return m.ID, m.Name, m.Description
				}),
			}, nil
		},
		setItem: func(ctx context.Context, chat domain.ChatRef, id string) error {
			return mm.SetSessionModel(ctx, chat, id)
		},
	}
}

func modeSwitchableConfig(mm domain.ModeManager) switchableConfig {
	return switchableConfig{
		noun:            "mode",
		errNotSupported: domain.ErrModesNotSupported,
		errNotFound:     domain.ErrModeNotFound,
		getState: func(chat domain.ChatRef) (*switchableState, error) {
			ms, err := mm.GetModeState(chat)
			if err != nil {
				return nil, err
			}
			return &switchableState{
				CurrentID: ms.CurrentModeID,
				Available: toSwitchableItems(ms.Available, func(m domain.ModeInfo) (string, string, string) {
					return m.ID, m.Name, m.Description
				}),
			}, nil
		},
		setItem: func(ctx context.Context, chat domain.ChatRef, id string) error {
			return mm.SetSessionMode(ctx, chat, id)
		},
	}
}

type switchableCommand struct {
	cfg switchableConfig
}

func (c *switchableCommand) Name() string { return c.cfg.noun }
func (c *switchableCommand) Description() string {
	return "List or switch " + c.cfg.noun + " (by ID or number)"
}

func (c *switchableCommand) Execute(
	ctx context.Context,
	args []string,
	tc *domain.TurnContext,
) (*domain.Result, error) {
	if len(args) == 0 {
		return switchableList(c.cfg, tc)
	}
	return switchableSwitch(c.cfg, ctx, args[0], tc)
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// switchableItem is a generic item (model or mode) with an ID, name, and optional description.
type switchableItem struct {
	ID          string
	Name        string
	Description string
}

// switchableState holds the current selection and available items.
type switchableState struct {
	CurrentID string
	Available []switchableItem
}

// switchableConfig provides the label text and callbacks for a switchable command.
type switchableConfig struct {
	noun            string // e.g. "model", "mode"
	errNotSupported error
	errNotFound     error
	getState        func(chat domain.ChatRef) (*switchableState, error)
	setItem         func(ctx context.Context, chat domain.ChatRef, id string) error
}

var errSwitchableIndexOutOfRange = errors.New("index out of range")

func switchableList(cfg switchableConfig, tc *domain.TurnContext) (*domain.Result, error) {
	state, err := cfg.getState(tc.Chat)
	if err != nil {
		return &domain.Result{Text: switchableErrToMessage(cfg, err, "", "Failed to get "+cfg.noun+" state.")}, nil
	}

	if len(state.Available) == 0 {
		return &domain.Result{Text: fmt.Sprintf("No %ss available.", cfg.noun)}, nil
	}

	lines := []string{fmt.Sprintf("Available %ss", upperFirst(cfg.noun)), ""}
	for i, item := range state.Available {
		marker := "  "
		if item.ID == state.CurrentID {
			marker = "▶ "
		}
		line := fmt.Sprintf("%s%d. %s", marker, i+1, item.Name)
		if item.Description != "" {
			line += " — " + item.Description
		}
		line += fmt.Sprintf("\n     ID: `%s`", item.ID)
		lines = append(lines, line)
	}
	lines = append(lines, "", fmt.Sprintf("Use `/%s <ID>` or `/%s <number>` to switch.", cfg.noun, cfg.noun))
	return &domain.Result{Text: strings.Join(lines, "\n")}, nil
}

func switchableSwitch(
	cfg switchableConfig,
	ctx context.Context,
	arg string,
	tc *domain.TurnContext,
) (*domain.Result, error) {
	hint := fmt.Sprintf(". Use /%s to list available %ss.", cfg.noun, cfg.noun)

	id, err := switchableResolveID(cfg, arg, tc)
	if err != nil {
		if errors.Is(err, errSwitchableIndexOutOfRange) {
			return &domain.Result{Text: err.Error() + hint}, nil
		}
		return &domain.Result{Text: switchableErrToMessage(cfg, err, "", "Failed to get "+cfg.noun+" state.")}, nil
	}

	if err := cfg.setItem(ctx, tc.Chat, id); err != nil {
		return &domain.Result{Text: switchableErrToMessage(cfg, err, id, "Failed to switch "+cfg.noun+".")}, nil
	}
	return &domain.Result{Text: fmt.Sprintf("Switched to %s `%s`.", cfg.noun, id)}, nil
}

func switchableErrToMessage(cfg switchableConfig, err error, id string, fallback string) string {
	if err == nil {
		return fallback
	}
	hint := fmt.Sprintf(". Use /%s to list available %ss.", cfg.noun, cfg.noun)
	switch {
	case errors.Is(err, domain.ErrNoActiveSession):
		return "No active session. Use /new first."
	case errors.Is(err, cfg.errNotSupported):
		return fmt.Sprintf("Agent does not support %s switching.", cfg.noun)
	case errors.Is(err, cfg.errNotFound):
		return fmt.Sprintf("%s `%s` not found%s", upperFirst(cfg.noun), id, hint)
	default:
		return fallback
	}
}

func switchableResolveID(cfg switchableConfig, arg string, tc *domain.TurnContext) (string, error) {
	n, parseErr := strconv.Atoi(arg)
	if parseErr != nil {
		return arg, nil
	}
	state, err := cfg.getState(tc.Chat)
	if err != nil {
		return "", err
	}
	if n < 1 || n > len(state.Available) {
		return "", fmt.Errorf("%w: %d (valid: 1-%d)", errSwitchableIndexOutOfRange, n, len(state.Available))
	}
	return state.Available[n-1].ID, nil
}
