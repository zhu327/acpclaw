package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

// mockCronStore is an in-memory implementation of CronStore for testing.
type mockCronStore struct {
	jobs []domain.CronJob
}

func newMockCronStore() *mockCronStore {
	return &mockCronStore{jobs: make([]domain.CronJob, 0)}
}

func (m *mockCronStore) AddJob(job domain.CronJob) error {
	m.jobs = append(m.jobs, job)
	return nil
}

func (m *mockCronStore) LoadJobs(channel, chatID string) ([]domain.CronJob, error) {
	var out []domain.CronJob
	for _, j := range m.jobs {
		if j.Channel == channel && j.ChatID == chatID {
			out = append(out, j)
		}
	}
	return out, nil
}

func (m *mockCronStore) DeleteJob(channel, chatID, jobID string) error {
	filtered := make([]domain.CronJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.Channel != channel || j.ChatID != chatID || j.ID != jobID {
			filtered = append(filtered, j)
		}
	}
	m.jobs = filtered
	return nil
}

func (m *mockCronStore) ListAllJobs() ([]domain.CronJob, error) {
	return append([]domain.CronJob(nil), m.jobs...), nil
}

func makeCronRequest(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func cronCreateArgs(channel, chatID string) map[string]any {
	return map[string]any{"channel": channel, "chatId": chatID}
}

func TestCronCreateHandler(t *testing.T) {
	cronStore := newMockCronStore()
	handler := cronCreateHandler(cronStore)
	ctx := context.Background()

	t.Run("missing channel", func(t *testing.T) {
		req := makeCronRequest("cron_create", map[string]any{
			"message":  "hello",
			"cronExpr": "0 9 * * *",
			"chatId":   "123",
		})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "channel and chatId")
	})

	t.Run("missing message", func(t *testing.T) {
		req := makeCronRequest("cron_create", cronCreateArgs("telegram", "123"))
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "message is required")
	})

	t.Run("missing cronExpr and runAt", func(t *testing.T) {
		req := makeCronRequest("cron_create", map[string]any{
			"message": "hello",
			"channel": "telegram",
			"chatId":  "123",
		})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "cronExpr or runAt")
	})

	t.Run("create with cronExpr", func(t *testing.T) {
		args := cronCreateArgs("telegram", "123")
		args["message"] = "remind me"
		args["cronExpr"] = "0 9 * * 1-5"
		args["label"] = "weekday reminder"
		req := makeCronRequest("cron_create", args)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "Created job")
		jobs, err := cronStore.LoadJobs("telegram", "123")
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		assert.Equal(t, "remind me", jobs[0].Message)
		assert.Equal(t, "0 9 * * 1-5", jobs[0].CronExpr)
		assert.Equal(t, "weekday reminder", jobs[0].Label)
	})

	t.Run("create with runAt", func(t *testing.T) {
		runAt := time.Now().Add(time.Hour).Format(time.RFC3339)
		args := cronCreateArgs("telegram", "123")
		args["message"] = "one-time"
		args["runAt"] = runAt
		req := makeCronRequest("cron_create", args)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		jobs, err := cronStore.LoadJobs("telegram", "123")
		require.NoError(t, err)
		require.Len(t, jobs, 2)
		assert.NotNil(t, jobs[1].RunAt)
	})
}

func TestCronListHandler(t *testing.T) {
	cronStore := newMockCronStore()
	require.NoError(t, cronStore.AddJob(domain.CronJob{
		ID: "j1", Channel: "telegram", ChatID: "123", Message: "m1", Label: "L1", Enabled: true,
	}))
	handler := cronListHandler(cronStore)
	ctx := context.Background()

	req := makeCronRequest("cron_list", cronCreateArgs("telegram", "123"))
	res, err := handler(ctx, req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcp.TextContent).Text
	assert.Contains(t, text, "j1")
	assert.Contains(t, text, "L1")
	assert.Contains(t, text, "Enabled: true")
}

func TestCronDeleteHandler(t *testing.T) {
	cronStore := newMockCronStore()
	require.NoError(t, cronStore.AddJob(domain.CronJob{ID: "j1", Channel: "telegram", ChatID: "123", Message: "m1"}))
	handler := cronDeleteHandler(cronStore)
	ctx := context.Background()

	t.Run("missing id", func(t *testing.T) {
		req := makeCronRequest("cron_delete", cronCreateArgs("telegram", "123"))
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "id is required")
	})

	t.Run("delete", func(t *testing.T) {
		args := cronCreateArgs("telegram", "123")
		args["id"] = "j1"
		req := makeCronRequest("cron_delete", args)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "Deleted job j1")
		jobs, err := cronStore.LoadJobs("telegram", "123")
		require.NoError(t, err)
		assert.Len(t, jobs, 0)
	})
}
