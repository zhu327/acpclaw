package domain

import "time"

type SessionContext struct {
	Channel   string    `json:"channel"`
	ChatID    string    `json:"chatId"`
	UpdatedAt time.Time `json:"updatedAt"`
}
