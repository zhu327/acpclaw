package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

// promptJob is one queued user/agent turn (FIFO per chat).
type promptJob struct {
	action domain.Action
	tc     *domain.TurnContext
}

// promptQueueManager holds a bounded FIFO per chat for jobs not yet passed to Prompter.Prompt.
// Idle workers exit and chat entries are removed from the map after idleTimeout with a reclaim handshake.
type promptQueueManager struct {
	maxQueued   int
	idleTimeout time.Duration
	now         func() time.Time
	mu          sync.Mutex
	chats       map[string]*chatQueue
	parentCtx   context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	run         func(context.Context, *promptJob)
}

func newPromptQueueManager(
	maxQueued int,
	idleTimeout time.Duration,
	now func() time.Time,
	parentCtx context.Context,
	run func(context.Context, *promptJob),
) *promptQueueManager {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(parentCtx)
	return &promptQueueManager{
		maxQueued:   maxQueued,
		idleTimeout: idleTimeout,
		now:         now,
		chats:       make(map[string]*chatQueue),
		parentCtx:   ctx,
		cancel:      cancel,
		run:         run,
	}
}

type chatQueue struct {
	chatKey string

	mu           sync.Mutex
	cond         *sync.Cond
	pending      []*promptJob
	started      bool
	closed       bool
	idleSince    time.Time
	reclaiming   bool
	detached     bool
	runningToken string
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

// Submit enqueues a job. Returns false if the queue is full or the manager is shutting down.
func (m *promptQueueManager) Submit(job *promptJob) bool {
	select {
	case <-m.parentCtx.Done():
		return false
	default:
	}

	key := job.tc.Chat.CompositeKey()
	for {
		m.mu.Lock()
		cq := m.chats[key]
		if cq == nil {
			cq = newChatQueue(key)
			m.chats[key] = cq
		}
		m.mu.Unlock()

		cq.mu.Lock()
		if cq.reclaiming {
			cq.mu.Unlock()
			runtime.Gosched()
			continue
		}
		if cq.detached {
			cq.mu.Unlock()
			continue
		}
		if cq.closed {
			cq.mu.Unlock()
			return false
		}
		if len(cq.pending) >= m.maxQueued {
			cq.mu.Unlock()
			return false
		}
		cq.pending = append(cq.pending, job)
		cq.idleSince = time.Time{}
		cq.cond.Signal()
		cq.mu.Unlock()

		m.ensureWorker(cq)
		return true
	}
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
			if m.tryIdleReclaimLocked(cq) {
				return
			}
			m.waitWithIdleTimeout(cq)
		}
		if cq.closed && len(cq.pending) == 0 {
			cq.mu.Unlock()
			return
		}

		job := cq.pending[0]
		cq.pending = cq.pending[1:]

		cq.runningToken = randomRunningToken()
		cq.mu.Unlock()

		m.invokeRun(cq, m.parentCtx, job)
	}
}

// tryIdleReclaimLocked returns true if this worker should exit (reclaimed).
// On entry cq.mu must be held. On return false, cq.mu is held again. On return true, cq.mu is not held.
func (m *promptQueueManager) tryIdleReclaimLocked(cq *chatQueue) bool {
	if m.idleTimeout <= 0 || cq.idleSince.IsZero() {
		return false
	}
	if m.now().Sub(cq.idleSince) < m.idleTimeout {
		return false
	}
	cq.reclaiming = true
	key := cq.chatKey
	cq.mu.Unlock()

	m.mu.Lock()
	ok := m.chats[key] == cq
	if ok {
		delete(m.chats, key)
	}
	m.mu.Unlock()

	cq.mu.Lock()
	cq.reclaiming = false
	cq.detached = true
	cq.started = false
	cq.mu.Unlock()
	// Always exit this worker after a reclaim attempt: either we removed this queue from the map,
	// or another Submit replaced the map entry and this goroutine must not keep serving a dead queue.
	return true
}

func (m *promptQueueManager) waitWithIdleTimeout(cq *chatQueue) {
	if m.idleTimeout <= 0 || cq.idleSince.IsZero() {
		cq.cond.Wait()
		return
	}
	d := m.idleTimeout - m.now().Sub(cq.idleSince)
	if d <= 0 {
		return
	}
	timer := time.AfterFunc(d, func() { cq.cond.Broadcast() })
	cq.cond.Wait()
	timer.Stop()
}

func (m *promptQueueManager) invokeRun(cq *chatQueue, ctx context.Context, job *promptJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("prompt job panicked", "recover", r)
		}
		cq.mu.Lock()
		cq.runningToken = ""
		if len(cq.pending) == 0 && !cq.closed {
			cq.idleSince = m.now()
		}
		cq.mu.Unlock()
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
