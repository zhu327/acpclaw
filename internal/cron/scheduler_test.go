package cron

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduler_Trigger(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now()
	runAt := now.Add(-1 * time.Minute) // Should trigger immediately

	job := Job{
		ID:      "test-1",
		Channel: "telegram",
		ChatID:  "123",
		Message: "hello",
		Enabled: true,
		RunAt:   &runAt,
	}
	require.NoError(t, store.AddJob(job))

	scheduler := NewScheduler(store, 10*time.Millisecond)

	triggered := make(chan Job, 1)
	scheduler.OnTrigger(func(j Job) {
		triggered <- j
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go scheduler.Start(ctx)

	select {
	case j := <-triggered:
		require.Equal(t, "test-1", j.ID)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for trigger")
	}

	// Verify job was disabled
	jobs, _ := store.LoadJobs("telegram", "123")
	require.Len(t, jobs, 1)
	require.False(t, jobs[0].Enabled)
}
