package builtin

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/domain"
)

func TestPromptQueue_SubmitRejectsWhenFull(t *testing.T) {
	blocked := make(chan struct{})
	inRun := make(chan struct{})
	var once sync.Once

	run := func(ctx context.Context, job *promptJob) {
		once.Do(func() { close(inRun) })
		<-blocked
	}

	q := newPromptQueueManager(2, context.Background(), run)
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

	q := newPromptQueueManager(5, context.Background(), run)
	chat := domain.ChatRef{ChannelKind: "test", ChatID: "x"}
	tc := &domain.TurnContext{Chat: chat, State: domain.State{}}

	require.True(t, q.Submit(&promptJob{tc: tc}))
	<-inRun
	require.True(t, q.Submit(&promptJob{tc: tc}))
	require.True(t, q.Submit(&promptJob{tc: tc}))

	n := q.Drain(chat.CompositeKey())
	assert.Equal(t, 2, n)

	close(blocked)
	q.Shutdown()
}
