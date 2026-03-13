package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zhu327/acpclaw/internal/domain"
)

// Store manages cron jobs persistence.
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore creates a new Store.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) getPath(channel, chatID string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s_%s.json", channel, chatID))
}

// LoadJobs loads all jobs for a specific channel and chat ID.
func (s *Store) LoadJobs(channel, chatID string) ([]domain.CronJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadJobsLocked(channel, chatID)
}

func (s *Store) loadJobsLocked(channel, chatID string) ([]domain.CronJob, error) {
	path := s.getPath(channel, chatID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.CronJob{}, nil
		}
		return nil, err
	}
	var jobs []domain.CronJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// SaveJobs saves all jobs for a specific channel and chat ID.
func (s *Store) SaveJobs(channel, chatID string, jobs []domain.CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveJobsLocked(channel, chatID, jobs)
}

func (s *Store) saveJobsLocked(channel, chatID string, jobs []domain.CronJob) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := s.getPath(channel, chatID)
	if len(jobs) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// AddJob adds a new job.
func (s *Store) AddJob(job domain.CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.loadJobsLocked(job.Channel, job.ChatID)
	if err != nil {
		return err
	}
	jobs = append(jobs, job)
	return s.saveJobsLocked(job.Channel, job.ChatID, jobs)
}

// UpdateJob updates an existing job.
func (s *Store) UpdateJob(job domain.CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.loadJobsLocked(job.Channel, job.ChatID)
	if err != nil {
		return err
	}
	found := false
	for i, j := range jobs {
		if j.ID == job.ID {
			jobs[i] = job
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("job %s not found", job.ID)
	}
	return s.saveJobsLocked(job.Channel, job.ChatID, jobs)
}

// DeleteJob deletes a job.
func (s *Store) DeleteJob(channel, chatID, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.loadJobsLocked(channel, chatID)
	if err != nil {
		return err
	}

	var updated []domain.CronJob
	found := false
	for _, j := range jobs {
		if j.ID != jobID {
			updated = append(updated, j)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("job %s not found", jobID)
	}

	return s.saveJobsLocked(channel, chatID, updated)
}

// ListAllJobs returns all jobs across all files.
func (s *Store) ListAllJobs() ([]domain.CronJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var allJobs []domain.CronJob
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var jobs []domain.CronJob
		if err := json.Unmarshal(data, &jobs); err == nil {
			allJobs = append(allJobs, jobs...)
		}
	}
	return allJobs, nil
}
