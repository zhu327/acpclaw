package telegram

import "strings"

// AllowlistChecker checks whether a user is allowed to interact with the bot.
type AllowlistChecker interface {
	IsAllowed(userID int64, username string) bool
}

// AllowlistConfig holds user ID and username allowlists for access control.
type AllowlistConfig struct {
	AllowedUserIDs   []int64
	AllowedUsernames []string
}

// DefaultAllowlistChecker checks access using configured allowlists.
type DefaultAllowlistChecker struct {
	cfg AllowlistConfig
}

// NewAllowlistChecker creates an allowlist checker.
func NewAllowlistChecker(cfg AllowlistConfig) *DefaultAllowlistChecker {
	return &DefaultAllowlistChecker{cfg: cfg}
}

// IsAllowed returns true when the user is in the allowlist, or when both lists are empty.
func (c *DefaultAllowlistChecker) IsAllowed(userID int64, username string) bool {
	if len(c.cfg.AllowedUserIDs) == 0 && len(c.cfg.AllowedUsernames) == 0 {
		return true
	}
	for _, id := range c.cfg.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	usernameLower := strings.ToLower(strings.TrimSpace(username))
	for _, u := range c.cfg.AllowedUsernames {
		if strings.ToLower(strings.TrimSpace(u)) == usernameLower {
			return true
		}
	}
	return false
}
