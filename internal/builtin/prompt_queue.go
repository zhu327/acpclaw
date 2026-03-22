package builtin

import (
	"context"
	"log/slog"
	"sync"

	"github.com/zhu327/acpclaw/internal/domain"
)

// promptJob is one queued user/agent turn (FIFO per chat).
type promptJob struct {
	action domain.Action
	tc     *domain.TurnContext
}

// promptQueueManager holds a bounded FIFO per chat for jobs not yet passed to Prompter.Prompt.
// Each chat uses a slice guarded by mutex so Drain and Submit serialize correctly.
// The chats map is not pruned when idle; long-running bots with many unique chats may grow memory (see monitoring if needed).
type promptQueueManager struct {
	maxQueued int
	mu        sync.Mutex
	chats     map[string]*chatQueue
	parentCtx context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	run       func(context.Context, *promptJob)
}

func newPromptQueueManager(
	maxQueued int,
	parentCtx context.Context,
	run func(context.Context, *promptJob),
) *promptQueueManager {
	ctx, cancel := context.WithCancel(parentCtx)
	return &promptQueueManager{
		maxQueued: maxQueued,
		chats:     make(map[string]*chatQueue),
		parentCtx: ctx,
		cancel:    cancel,
		run:       run,
	}
}

type chatQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []*promptJob
	started bool
	closed  bool
}

func newChatQueue() *chatQueue {
	cq := &chatQueue{}
	cq.cond = sync.NewCond(&cq.mu)
	return cq
}

// Submit enqueues a job. Returns false if the queue is full or the manager is shutting down.
func (m *promptQueueManager) Submit(job *promptJob) bool {
	select {
	case <-m.parentCtx.Done():
		return false
	default:
	}

	key := job.tc.Chat.CompositeKey()
	m.mu.Lock()
	cq := m.chats[key]
	if cq == nil {
		cq = newChatQueue()
		m.chats[key] = cq
	}
	m.mu.Unlock()

	if !cq.push(job, m.maxQueued) {
		return false
	}
	m.ensureWorker(cq)
	return true
}

func (cq *chatQueue) push(job *promptJob, max int) bool {
	cq.mu.Lock()
	defer cq.mu.Unlock()
	if cq.closed {
		return false
	}
	if len(cq.pending) >= max {
		return false
	}
	cq.pending = append(cq.pending, job)
	cq.cond.Signal()
	return true
}

func (m *promptQueueManager) ensureWorker(cq *chatQueue) {
	cq.mu.Lock()
	if cq.started {
		cq.mu.Unlock()
		return
	}
	cq.started = true
	cq.mu.Unlock()
	m.wg.Add(1)
	go m.workerLoop(cq)
}

func (m *promptQueueManager) workerLoop(cq *chatQueue) {
	defer m.wg.Done()
	for {
		cq.mu.Lock()
		for len(cq.pending) == 0 && !cq.closed {
			cq.cond.Wait()
		}
		if cq.closed && len(cq.pending) == 0 {
			cq.mu.Unlock()
			return
		}
		job := cq.pending[0]
		cq.pending = cq.pending[1:]
		cq.mu.Unlock()

		m.invokeRun(job)
	}
}

func (m *promptQueueManager) invokeRun(job *promptJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("prompt job panicked", "recover", r)
		}
	}()
	m.run(m.parentCtx, job)
}

// Drain removes jobs not yet started for the chat. Returns how many were dropped.
func (m *promptQueueManager) Drain(chatKey string) int {
	m.mu.Lock()
	cq := m.chats[chatKey]
	m.mu.Unlock()
	if cq == nil {
		return 0
	}
	cq.mu.Lock()
	defer cq.mu.Unlock()
	n := len(cq.pending)
	cq.pending = nil
	return n
}

// Shutdown signals workers to stop and waits for them.
func (m *promptQueueManager) Shutdown() {
	m.mu.Lock()
	for _, cq := range m.chats {
		cq.mu.Lock()
		cq.closed = true
		cq.cond.Broadcast()
		cq.mu.Unlock()
	}
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// logQueueFullRejected logs a non-interactive rejection (e.g. cron) when the queue is full.
func logQueueFullRejected(chat domain.ChatRef, source string) {
	slog.Warn("prompt queue full, submission rejected",
		"chat", chat.CompositeKey(),
		"source", source,
	)
}
