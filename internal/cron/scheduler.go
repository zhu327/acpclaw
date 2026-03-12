package cron

import (
	"context"
	"log/slog"
	"time"

	robfig "github.com/robfig/cron/v3"
)

// Scheduler polls and triggers cron jobs.
type Scheduler struct {
	store     *Store
	interval  time.Duration
	onTrigger func(Job)
}

// NewScheduler creates a new Scheduler.
func NewScheduler(store *Store, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:    store,
		interval: interval,
	}
}

// OnTrigger registers the callback for triggered jobs.
func (s *Scheduler) OnTrigger(fn func(Job)) {
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
				go s.onTrigger(job)
			}
		}
	}
}
