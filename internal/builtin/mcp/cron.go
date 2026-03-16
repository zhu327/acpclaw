package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/domain"
)

const (
	channelDesc = "Channel from [Session Info], e.g. 'telegram'"
	chatIDDesc  = "Chat ID from [Session Info], e.g. '123456789'"
)

func parseChannelAndChatID(req mcp.CallToolRequest) (channel, chatID string, errResult *mcp.CallToolResult) {
	channel = mcp.ParseString(req, "channel", "")
	chatID = mcp.ParseString(req, "chatId", "")
	if channel == "" || chatID == "" {
		return "", "", mcp.NewToolResultError("channel and chatId are required")
	}
	return channel, chatID, nil
}

func cronCreateTool() mcp.Tool {
	return mcp.NewTool(
		"cron_create",
		mcp.WithDescription(
			"Create a scheduled task. When triggered, 'message' is sent as a prompt to the AI agent and the agent's reply is delivered to the user. "+
				"Use channel and chatId from the [Session Info] provided at the start of this conversation. "+
				"Either cronExpr or runAt is required.",
		),
		mcp.WithString(
			"message",
			mcp.Required(),
			mcp.Description(
				"Instruction for the AI agent when triggered. The agent's response is sent to the user. Example: 'Send a friendly good morning greeting to the user.'",
			),
		),
		mcp.WithString("channel", mcp.Required(), mcp.Description(channelDesc)),
		mcp.WithString("chatId", mcp.Required(), mcp.Description(chatIDDesc)),
		mcp.WithString(
			"cronExpr",
			mcp.Description(
				"5-field cron expression (minute hour day month weekday). Examples: '0 9 * * *' = daily 9am, '0 9 * * 1-5' = weekdays 9am",
			),
		),
		mcp.WithString(
			"runAt",
			mcp.Description(
				"RFC3339 timestamp for a one-time task, e.g. '2026-01-01T09:00:00+08:00'. Use instead of cronExpr for non-recurring tasks",
			),
		),
		mcp.WithString("label", mcp.Description("Short description of the task, e.g. 'Daily morning greeting'")),
	)
}

func cronCreateHandler(store CronStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channel, chatID, errRes := parseChannelAndChatID(req)
		if errRes != nil {
			return errRes, nil
		}
		message := mcp.ParseString(req, "message", "")
		if message == "" {
			return mcp.NewToolResultError("message is required"), nil
		}
		job, errRes := buildCronJobFromRequest(req, channel, chatID, message)
		if errRes != nil {
			return errRes, nil
		}
		if err := store.AddJob(job); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Created job %s", job.ID)), nil
	}
}

func buildCronJobFromRequest(
	req mcp.CallToolRequest,
	channel, chatID, message string,
) (domain.CronJob, *mcp.CallToolResult) {
	job := domain.CronJob{
		ID:        uuid.New().String(),
		Channel:   channel,
		ChatID:    chatID,
		Message:   message,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if label := mcp.ParseString(req, "label", ""); label != "" {
		job.Label = label
	}
	if expr := mcp.ParseString(req, "cronExpr", ""); expr != "" {
		job.CronExpr = expr
	}
	if runAtStr := mcp.ParseString(req, "runAt", ""); runAtStr != "" {
		t, err := time.Parse(time.RFC3339, runAtStr)
		if err != nil {
			return domain.CronJob{}, mcp.NewToolResultError("invalid runAt format, use ISO8601/RFC3339")
		}
		job.RunAt = &t
	}
	if job.CronExpr == "" && job.RunAt == nil {
		return domain.CronJob{}, mcp.NewToolResultError("either cronExpr or runAt is required")
	}
	return job, nil
}

func cronListTool() mcp.Tool {
	return mcp.NewTool(
		"cron_list",
		mcp.WithDescription(
			"List all scheduled tasks for the current chat. Use channel and chatId from [Session Info].",
		),
		mcp.WithString("channel", mcp.Required(), mcp.Description(channelDesc)),
		mcp.WithString("chatId", mcp.Required(), mcp.Description(chatIDDesc)),
	)
}

func cronListHandler(store CronStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channel, chatID, errRes := parseChannelAndChatID(req)
		if errRes != nil {
			return errRes, nil
		}
		jobs, err := store.LoadJobs(channel, chatID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lines := make([]string, len(jobs))
		for i, j := range jobs {
			lines[i] = fmt.Sprintf("- [%s] %s (Enabled: %v)", j.ID, j.Label, j.Enabled)
		}
		res := strings.Join(lines, "\n")
		if res == "" {
			res = "No jobs found."
		}
		return mcp.NewToolResultText(res), nil
	}
}

func cronDeleteTool() mcp.Tool {
	return mcp.NewTool("cron_delete",
		mcp.WithDescription("Delete a scheduled task by ID. Use cron_list first to find the job ID."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Job ID (UUID) obtained from cron_list")),
		mcp.WithString("channel", mcp.Required(), mcp.Description(channelDesc)),
		mcp.WithString("chatId", mcp.Required(), mcp.Description(chatIDDesc)),
	)
}

func cronDeleteHandler(store CronStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		channel, chatID, errRes := parseChannelAndChatID(req)
		if errRes != nil {
			return errRes, nil
		}
		if err := store.DeleteJob(channel, chatID, id); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Deleted job %s", id)), nil
	}
}
