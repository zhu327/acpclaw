package builtin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

// drainPendingForTest clears the not-yet-started slice only (no cancelRequested); production uses CancelAndDrain.
func drainPendingForTest(m *promptQueueManager, key string) int {
	cq := m.chatQueueFor(key)
	if cq == nil {
		return 0
	}
	cq.mu.Lock()
	defer cq.mu.Unlock()
	n := len(cq.pending)
	cq.pending = nil
	cq.cond.Broadcast()
	return n
}

func testQueue(t *testing.T, maxQueued int, run func(context.Context, *promptJob)) *promptQueueManager {
	t.Helper()
	return newPromptQueueManager(maxQueued, time.Hour, nil, context.Background(), run)
}

func TestPromptQueue_SubmitRejectsWhenFull(t *testing.T) {
	blocked := make(chan struct{})
	inRun := make(chan struct{})
	var once sync.Once

	run := func(ctx context.Context, job *promptJob) {
		once.Do(func() { close(inRun) })
		<-blocked
	}

	q := testQueue(t, 2, run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "1"}
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	require.True(t, q.Submit(&promptJob{tc: tc}))
	<-inRun
	require.True(t, q.Submit(&promptJob{tc: tc}))
	require.True(t, q.Submit(&promptJob{tc: tc}))
	require.False(t, q.Submit(&promptJob{tc: tc}))

	close(blocked)
	q.Shutdown()
}

func TestPromptQueue_DrainClearsPending(t *testing.T) {
	blocked := make(chan struct{})
	inRun := make(chan struct{})
	var once sync.Once

	run := func(ctx context.Context, job *promptJob) {
		once.Do(func() { close(inRun) })
		<-blocked
	}

	q := testQueue(t, 5, run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "x"}
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	require.True(t, q.Submit(&promptJob{tc: tc}))
	<-inRun
	require.True(t, q.Submit(&promptJob{tc: tc}))
	require.True(t, q.Submit(&promptJob{tc: tc}))

	n := drainPendingForTest(q, chat.CompositeKey())
	assert.Equal(t, 2, n)

	close(blocked)
	q.Shutdown()
}

func TestPromptQueue_IdleReclaimAllowsResubmit(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	runCount := 0
	run := func(ctx context.Context, job *promptJob) {
		mu.Lock()
		runCount++
		mu.Unlock()
		wg.Done()
	}
	q := newPromptQueueManager(5, 25*time.Millisecond, nil, context.Background(), run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "idle1"}
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	wg.Add(1)
	require.True(t, q.Submit(&promptJob{tc: tc}))
	wg.Wait()

	time.Sleep(60 * time.Millisecond)

	wg.Add(1)
	require.True(t, q.Submit(&promptJob{tc: tc}))
	wg.Wait()

	mu.Lock()
	assert.Equal(t, 2, runCount)
	mu.Unlock()
	q.Shutdown()
}

func TestPromptQueue_CancelAndDrain(t *testing.T) {
	blocked := make(chan struct{})
	inRun := make(chan struct{})
	var once sync.Once
	run := func(ctx context.Context, job *promptJob) {
		once.Do(func() { close(inRun) })
		<-blocked
	}
	q := testQueue(t, 5, run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "c1"}
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	require.True(t, q.Submit(&promptJob{tc: tc}))
	<-inRun
	require.True(t, q.Submit(&promptJob{tc: tc}))

	n, err := q.CancelAndDrain(chat.CompositeKey(), func() error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	close(blocked)
	q.Shutdown()
}

func TestPromptQueue_BusyTokenMatches(t *testing.T) {
	blocked := make(chan struct{})
	inRun := make(chan struct{})
	var once sync.Once
	run := func(ctx context.Context, job *promptJob) {
		once.Do(func() { close(inRun) })
		<-blocked
	}
	q := testQueue(t, 5, run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "b1"}
	key := chat.CompositeKey()
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	require.True(t, q.Submit(&promptJob{tc: tc}))
	<-inRun

	q.mu.Lock()
	cq := q.chats[key]
	q.mu.Unlock()
	require.NotNil(t, cq)
	cq.mu.Lock()
	tok := cq.runningToken
	cq.mu.Unlock()
	require.NotEmpty(t, tok)

	assert.True(t, q.BusyTokenMatches(key, tok))
	assert.False(t, q.BusyTokenMatches(key, "deadbeef"))
	assert.False(t, q.BusyTokenMatches(key, ""))

	close(blocked)
	q.Shutdown()
}
