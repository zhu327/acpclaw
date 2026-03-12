package util

import "strings"

// AllowlistConfig holds user ID and username allowlists for access control.
type AllowlistConfig struct {
	AllowedUserIDs   []int64
	AllowedUsernames []string
}

// IsAllowed returns true if the user is in the allowlist, or if both lists are empty (allow all).
func IsAllowed(cfg AllowlistConfig, userID int64, username string) bool {
	if len(cfg.AllowedUserIDs) == 0 && len(cfg.AllowedUsernames) == 0 {
		return true
	}
	for _, id := range cfg.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	usernameLower := strings.ToLower(strings.TrimSpace(username))
	for _, u := range cfg.AllowedUsernames {
		if strings.ToLower(strings.TrimSpace(u)) == usernameLower {
			return true
		}
	}
	return false
}
