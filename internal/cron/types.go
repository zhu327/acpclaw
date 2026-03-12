package cron

import "time"

// Job represents a scheduled task.
type Job struct {
	ID        string     `json:"id"`
	Channel   string     `json:"channel"`
	ChatID    string     `json:"chatId"`
	Label     string     `json:"label,omitempty"`
	Message   string     `json:"message"`
	Enabled   bool       `json:"enabled"`
	CronExpr  string     `json:"cronExpr,omitempty"`
	RunAt     *time.Time `json:"runAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
}
