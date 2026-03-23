package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
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
type promptQueueManager struct {
	maxQueued int
	mu        sync.Mutex
	stopped   bool
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
	chatKey string

	mu            sync.Mutex
	cond          *sync.Cond
	pending       []*promptJob
	started       bool
	closed        bool
	runningToken  string
	runningCancel context.CancelFunc
}

func newChatQueue(chatKey string) *chatQueue {
	cq := &chatQueue{chatKey: chatKey}
	cq.cond = sync.NewCond(&cq.mu)
	return cq
}

// randomRunningToken returns a 32-char hex token (16 random bytes). With "busy|" prefix fits Telegram 64-byte callback_data limit.
func randomRunningToken() string {
	var b [16]byte
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := io.ReadFull(rand.Reader, b[:]); err == nil {
			return hex.EncodeToString(b[:])
		}
	}
	slog.Error("crypto/rand: failed to read running token entropy after retries")
	panic("acpclaw: crypto/rand exhausted for running token")
}

// chatQueueFor returns the chat queue for key, or nil. Caller must not hold chatQueue.mu.
func (m *promptQueueManager) chatQueueFor(key string) *chatQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chats[key]
}

// submitEnqueueOutcome is the result of attempting to append one job under cq.mu.
type submitEnqueueOutcome int

const (
	submitEnqueueRejected submitEnqueueOutcome = iota
	submitEnqueueOK
)

// lookupOrCreateQueue returns the chat queue for key, creating it if needed. Returns nil if the manager is stopped.
func (m *promptQueueManager) lookupOrCreateQueue(key string) *chatQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}
	cq := m.chats[key]
	if cq == nil {
		cq = newChatQueue(key)
		m.chats[key] = cq
	}
	return cq
}

// tryEnqueueOnChatQueue appends job to cq if allowed. Caller must not hold m.mu.
func (m *promptQueueManager) tryEnqueueOnChatQueue(cq *chatQueue, job *promptJob) submitEnqueueOutcome {
	cq.mu.Lock()
	if cq.closed {
		cq.mu.Unlock()
		return submitEnqueueRejected
	}
	if len(cq.pending) >= m.maxQueued {
		cq.mu.Unlock()
		return submitEnqueueRejected
	}
	cq.pending = append(cq.pending, job)
	cq.cond.Signal()
	cq.mu.Unlock()
	return submitEnqueueOK
}

// Submit enqueues a job. Returns false if the queue is full or the manager is shutting down.
func (m *promptQueueManager) Submit(job *promptJob) bool {
	select {
	case <-m.parentCtx.Done():
		return false
	default:
	}

	key := job.tc.Chat.CompositeKey()
	cq := m.lookupOrCreateQueue(key)
	if cq == nil {
		return false
	}
	switch m.tryEnqueueOnChatQueue(cq, job) {
	case submitEnqueueRejected:
		return false
	case submitEnqueueOK:
		m.ensureWorker(cq)
		return true
	}
	return false
}

func (m *promptQueueManager) ensureWorker(cq *chatQueue) {
	cq.mu.Lock()
	if cq.started || cq.closed {
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

		cq.runningToken = randomRunningToken()
		jobCtx, jobCancel := context.WithCancel(m.parentCtx)
		cq.runningCancel = jobCancel
		cq.mu.Unlock()

		m.invokeRun(cq, jobCtx, job)
	}
}

func (m *promptQueueManager) invokeRun(cq *chatQueue, ctx context.Context, job *promptJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("prompt job panicked", "recover", r)
		}
		cq.mu.Lock()
		cq.runningToken = ""
		cancel := cq.runningCancel
		cq.runningCancel = nil
		cq.mu.Unlock()
		if cancel != nil {
			cancel() // detach child from parentCtx's children map; CancelFunc is idempotent (e.g. after CancelAndDrain)
		}
	}()
	m.run(ctx, job)
}

// CancelAndDrain drops queued jobs, then runs cancelRunning (e.g. prompter.Cancel).
func (m *promptQueueManager) CancelAndDrain(chatKey string, cancelRunning func() error) (drained int, err error) {
	cq := m.chatQueueFor(chatKey)
	if cq != nil {
		cq.mu.Lock()
		drained = len(cq.pending)
		cq.pending = nil
		if cq.runningCancel != nil {
			cq.runningCancel()
		}
		cq.cond.Broadcast()
		cq.mu.Unlock()
	}

	err = cancelRunning()
	return drained, err
}

// BusyTokenMatches reports whether token matches the running prompt token for this chat.
func (m *promptQueueManager) BusyTokenMatches(chatKey, token string) bool {
	if token == "" {
		return false
	}
	cq := m.chatQueueFor(chatKey)
	if cq == nil {
		return false
	}
	cq.mu.Lock()
	defer cq.mu.Unlock()
	return cq.runningToken != "" && cq.runningToken == token
}

// Shutdown signals workers to stop and waits for them.
func (m *promptQueueManager) Shutdown() {
	m.mu.Lock()
	m.stopped = true
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
