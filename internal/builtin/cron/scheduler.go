package cron

import (
	"context"
	"log/slog"
	"time"

	robfig "github.com/robfig/cron/v3"
	"github.com/zhu327/acpclaw/internal/domain"
)

// JobStore defines the interface required by Scheduler for polling and updating jobs.
// Accepting an interface enables unit testing without filesystem.
type JobStore interface {
	ListAllJobs() ([]domain.CronJob, error)
	UpdateJob(job domain.CronJob) error
}

// Scheduler polls and triggers cron jobs.
type Scheduler struct {
	store     JobStore
	interval  time.Duration
	onTrigger func(domain.CronJob)
}

// NewScheduler creates a new Scheduler.
func NewScheduler(store JobStore, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:    store,
		interval: interval,
	}
}

// OnTrigger registers the callback for triggered jobs.
func (s *Scheduler) OnTrigger(fn func(domain.CronJob)) {
	s.onTrigger = fn
}

// Start begins the polling loop. Blocks until ctx is canceled.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	parser := robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(parser)
		}
	}
}

func (s *Scheduler) tick(parser robfig.Parser) {
	jobs, err := s.store.ListAllJobs()
	if err != nil {
		slog.Error("failed to list cron jobs", "error", err)
		return
	}

	now := time.Now()
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}

		shouldTrigger := false

		if job.RunAt != nil {
			if now.After(*job.RunAt) && (job.LastRun == nil || job.LastRun.Before(*job.RunAt)) {
				shouldTrigger = true
				job.Enabled = false // One-time job
			}
		} else if job.CronExpr != "" {
			schedule, err := parser.Parse(job.CronExpr)
			if err != nil {
				slog.Warn("invalid cron expression", "id", job.ID, "expr", job.CronExpr)
				continue
			}

			lastRun := job.CreatedAt
			if job.LastRun != nil {
				lastRun = *job.LastRun
			}

			nextRun := schedule.Next(lastRun)
			if now.After(nextRun) {
				shouldTrigger = true
			}
		}

		if shouldTrigger {
			job.LastRun = &now
			if err := s.store.UpdateJob(job); err != nil {
				slog.Error("failed to update job", "id", job.ID, "error", err)
				continue
			}
			if s.onTrigger != nil {
				go func(j domain.CronJob) {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("cron trigger panicked", "job_id", j.ID, "panic", r)
						}
					}()
					s.onTrigger(j)
				}(job)
			}
		}
	}
}
