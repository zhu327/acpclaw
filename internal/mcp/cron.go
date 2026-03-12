package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	"github.com/zhu327/acpclaw/internal/session"
)

var errNoSession = errors.New("no active session context found, please ensure a session is active")

// sessionContextOrError fetches the active session context; on failure it returns
// an error result that the handler can return directly.
func sessionContextOrError(store SessionContextStore) (*session.Context, *mcp.CallToolResult) {
	ctx, err := store.Read()
	if err != nil {
		return nil, mcp.NewToolResultError(errNoSession.Error())
	}
	return ctx, nil
}

func parseStringArg(req mcp.CallToolRequest, name string) string {
	v, _ := mcp.ParseArgument(req, name, "").(string)
	return v
}

func cronCreateTool() mcp.Tool {
	return mcp.Tool{
		Name:        "cron_create",
		Description: "Create a new scheduled task that will send a message at specified times.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Prompt to send when the cron job triggers",
				},
				"cronExpr": map[string]interface{}{
					"type":        "string",
					"description": "Cron expression (e.g. '0 9 * * 1-5' for 9am weekdays)",
				},
				"runAt": map[string]interface{}{
					"type":        "string",
					"description": "ISO8601 time for one-time task (alternative to cronExpr)",
				},
				"label": map[string]interface{}{
					"type":        "string",
					"description": "Human-readable task description",
				},
			},
			Required: []string{"message"},
		},
	}
}

func cronCreateHandler(store CronStore, sessionStore SessionContextStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lastCtx, errRes := sessionContextOrError(sessionStore)
		if errRes != nil {
			return errRes, nil
		}

		message := parseStringArg(request, "message")
		if message == "" {
			return mcp.NewToolResultError("message is required"), nil
		}

		job := cron.Job{
			ID:        uuid.New().String(),
			Channel:   lastCtx.Channel,
			ChatID:    lastCtx.ChatID,
			Message:   message,
			Enabled:   true,
			CreatedAt: time.Now(),
		}

		if label := parseStringArg(request, "label"); label != "" {
			job.Label = label
		}
		if expr := parseStringArg(request, "cronExpr"); expr != "" {
			job.CronExpr = expr
		}
		if runAtStr := parseStringArg(request, "runAt"); runAtStr != "" {
			t, err := time.Parse(time.RFC3339, runAtStr)
			if err != nil {
				return mcp.NewToolResultError("invalid runAt format, use ISO8601/RFC3339"), nil
			}
			job.RunAt = &t
		}

		if job.CronExpr == "" && job.RunAt == nil {
			return mcp.NewToolResultError("either cronExpr or runAt is required"), nil
		}

		if err := store.AddJob(job); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Created job %s", job.ID)), nil
	}
}

func cronListTool() mcp.Tool {
	return mcp.Tool{
		Name:        "cron_list",
		Description: "List all scheduled tasks.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
			Required:   []string{},
		},
	}
}

func cronListHandler(store CronStore, sessionStore SessionContextStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lastCtx, errRes := sessionContextOrError(sessionStore)
		if errRes != nil {
			return errRes, nil
		}

		jobs, err := store.LoadJobs(lastCtx.Channel, lastCtx.ChatID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var lines []string
		for _, j := range jobs {
			lines = append(lines, fmt.Sprintf("- [%s] %s (Enabled: %v)", j.ID, j.Label, j.Enabled))
		}
		res := strings.Join(lines, "\n")
		if res == "" {
			res = "No jobs found."
		}
		return mcp.NewToolResultText(res), nil
	}
}

func cronDeleteTool() mcp.Tool {
	return mcp.Tool{
		Name:        "cron_delete",
		Description: "Delete a scheduled task.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"id": map[string]interface{}{
					"type":        "string",
					"description": "Job ID to delete",
				},
			},
			Required: []string{"id"},
		},
	}
}

func cronDeleteHandler(store CronStore, sessionStore SessionContextStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := parseStringArg(request, "id")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		lastCtx, errRes := sessionContextOrError(sessionStore)
		if errRes != nil {
			return errRes, nil
		}

		if err := store.DeleteJob(lastCtx.Channel, lastCtx.ChatID, id); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Deleted job %s", id)), nil
	}
}
