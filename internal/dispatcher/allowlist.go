package dispatcher

import "strings"

// AllowlistConfig holds user ID and username allowlists for access control.
type AllowlistConfig struct {
	AllowedUserIDs   []int64
	AllowedUsernames []string
}

// AllowlistChecker 检查用户是否在允许列表中
type AllowlistChecker interface {
	IsAllowed(userID int64, username string) bool
}

// DefaultAllowlistChecker 基于配置的允许列表检查器
type DefaultAllowlistChecker struct {
	cfg AllowlistConfig
}

// NewAllowlistChecker 创建允许列表检查器
func NewAllowlistChecker(cfg AllowlistConfig) *DefaultAllowlistChecker {
	return &DefaultAllowlistChecker{cfg: cfg}
}

// IsAllowed 检查用户是否在允许列表中，如果两个列表都为空则允许所有用户
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

// IsAllowed returns true if the user is in the allowlist, or if both lists are empty (allow all).
// Deprecated: 使用 AllowlistChecker 接口代替
func IsAllowed(cfg AllowlistConfig, userID int64, username string) bool {
	checker := NewAllowlistChecker(cfg)
	return checker.IsAllowed(userID, username)
}
