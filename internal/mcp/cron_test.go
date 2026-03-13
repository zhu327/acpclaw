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

// mockSessionContextStore returns a fixed session context.
type mockSessionContextStore struct {
	ctx *domain.SessionContext
}

func newMockSessionContextStore(channel, chatID string) *mockSessionContextStore {
	return &mockSessionContextStore{
		ctx: &domain.SessionContext{Channel: channel, ChatID: chatID},
	}
}

func (m *mockSessionContextStore) Read() (*domain.SessionContext, error) {
	return m.ctx, nil
}

func makeCronRequest(name string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func TestCronCreateHandler(t *testing.T) {
	cronStore := newMockCronStore()
	sessionStore := newMockSessionContextStore("telegram", "123")
	handler := cronCreateHandler(cronStore, sessionStore)
	ctx := context.Background()

	t.Run("missing message", func(t *testing.T) {
		req := makeCronRequest("cron_create", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "message is required")
	})

	t.Run("missing cronExpr and runAt", func(t *testing.T) {
		req := makeCronRequest("cron_create", map[string]any{"message": "hello"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "cronExpr or runAt")
	})

	t.Run("create with cronExpr", func(t *testing.T) {
		req := makeCronRequest("cron_create", map[string]any{
			"message":  "remind me",
			"cronExpr": "0 9 * * 1-5",
			"label":    "weekday reminder",
		})
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
		req := makeCronRequest("cron_create", map[string]any{
			"message": "one-time",
			"runAt":   runAt,
		})
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
	sessionStore := newMockSessionContextStore("telegram", "123")
	handler := cronListHandler(cronStore, sessionStore)
	ctx := context.Background()

	req := makeCronRequest("cron_list", nil)
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
	sessionStore := newMockSessionContextStore("telegram", "123")
	handler := cronDeleteHandler(cronStore, sessionStore)
	ctx := context.Background()

	t.Run("missing id", func(t *testing.T) {
		req := makeCronRequest("cron_delete", nil)
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "id is required")
	})

	t.Run("delete", func(t *testing.T) {
		req := makeCronRequest("cron_delete", map[string]any{"id": "j1"})
		res, err := handler(ctx, req)
		require.NoError(t, err)
		require.False(t, res.IsError)
		assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "Deleted job j1")
		jobs, err := cronStore.LoadJobs("telegram", "123")
		require.NoError(t, err)
		assert.Len(t, jobs, 0)
	})
}
