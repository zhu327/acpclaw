package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	job := Job{
		ID:        "test-id",
		Channel:   "telegram",
		ChatID:    "123",
		Message:   "hello",
		Enabled:   true,
		CreatedAt: time.Now(),
	}

	err := store.AddJob(job)
	require.NoError(t, err)

	jobs, err := store.LoadJobs("telegram", "123")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, job.ID, jobs[0].ID)
	require.Equal(t, job.Message, jobs[0].Message)

	allJobs, err := store.ListAllJobs()
	require.NoError(t, err)
	require.Len(t, allJobs, 1)

	err = store.DeleteJob("telegram", "123", "test-id")
	require.NoError(t, err)

	jobs, err = store.LoadJobs("telegram", "123")
	require.NoError(t, err)
	require.Len(t, jobs, 0)
}
