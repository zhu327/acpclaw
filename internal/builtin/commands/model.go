package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

// ModelCommand handles /model [modelId].
// Without arguments it lists available models; with an argument it switches to the specified model.
type ModelCommand struct {
	modelMgr domain.ModelManager
}

// NewModelCommand creates a ModelCommand.
func NewModelCommand(mm domain.ModelManager) *ModelCommand {
	return &ModelCommand{modelMgr: mm}
}

func (c *ModelCommand) Name() string        { return "model" }
func (c *ModelCommand) Description() string { return "List or switch model (by ID or number)" }

func (c *ModelCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	if len(args) == 0 {
		return c.listModels(tc)
	}
	return c.switchModel(ctx, args[0], tc)
}

func (c *ModelCommand) listModels(tc *domain.TurnContext) (*domain.Result, error) {
	state, err := c.modelMgr.GetModelState(tc.Chat)
	if err != nil {
		return &domain.Result{Text: modelErrToMessage(err, "", "Failed to get model state.")}, nil
	}

	if len(state.Available) == 0 {
		return &domain.Result{Text: "No models available."}, nil
	}

	lines := []string{"**Available Models**", ""}
	for i, m := range state.Available {
		marker := "  "
		if m.ID == state.CurrentModelID {
			marker = "▶ "
		}
		line := fmt.Sprintf("%s%d. %s", marker, i+1, m.Name)
		if m.Description != "" {
			line += " — " + m.Description
		}
		line += fmt.Sprintf("\n     ID: `%s`", m.ID)
		lines = append(lines, line)
	}
	lines = append(lines, "", "Use `/model <ID>` or `/model <number>` to switch.")
	return &domain.Result{Text: strings.Join(lines, "\n")}, nil
}

const hintListModels = ". Use /model to list available models."

func (c *ModelCommand) switchModel(ctx context.Context, arg string, tc *domain.TurnContext) (*domain.Result, error) {
	modelID, err := c.resolveModelID(arg, tc)
	if err != nil {
		if errors.Is(err, errModelIndexOutOfRange) {
			return &domain.Result{Text: err.Error() + hintListModels}, nil
		}
		return &domain.Result{Text: modelErrToMessage(err, "", "Failed to get model state.")}, nil
	}

	if err := c.modelMgr.SetSessionModel(ctx, tc.Chat, modelID); err != nil {
		return &domain.Result{Text: modelErrToMessage(err, modelID, "Failed to switch model.")}, nil
	}
	return &domain.Result{Text: fmt.Sprintf("Switched to model `%s`.", modelID)}, nil
}

// modelErrToMessage maps domain errors to user-facing messages.
func modelErrToMessage(err error, modelID string, fallback string) string {
	if err == nil {
		return fallback
	}
	switch {
	case errors.Is(err, domain.ErrNoActiveSession):
		return "No active session. Use /new first."
	case errors.Is(err, domain.ErrModelsNotSupported):
		return "Agent does not support model switching."
	case errors.Is(err, domain.ErrModelNotFound):
		return fmt.Sprintf("Model `%s` not found%s", modelID, hintListModels)
	default:
		return fallback
	}
}

var errModelIndexOutOfRange = errors.New("model index out of range")

// resolveModelID resolves a user argument to a model ID.
// It accepts either a numeric index (1-based) or a literal model ID.
func (c *ModelCommand) resolveModelID(arg string, tc *domain.TurnContext) (string, error) {
	n, parseErr := strconv.Atoi(arg)
	if parseErr != nil {
		return arg, nil
	}
	state, err := c.modelMgr.GetModelState(tc.Chat)
	if err != nil {
		return "", err
	}
	if n < 1 || n > len(state.Available) {
		return "", fmt.Errorf("%w: %d (valid: 1-%d)", errModelIndexOutOfRange, n, len(state.Available))
	}
	return state.Available[n-1].ID, nil
}
