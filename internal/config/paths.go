package config

import (
	"os"
	"path/filepath"
)

// GetAcpclawBaseDir returns the acpclaw base directory (~/.acpclaw).
// ACPCLAW_HOME overrides the default; if unset, uses ~/.acpclaw.
// If the user home directory cannot be resolved, fall back to the system temp directory.
func GetAcpclawBaseDir() string {
	if home := os.Getenv("ACPCLAW_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".acpclaw")
	}
	return filepath.Join(home, ".acpclaw")
}

// GetAcpclawMemoryDir returns the memory data directory.
// Note: callers must ensure the directory exists (use os.MkdirAll).
func GetAcpclawMemoryDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "memory")
}

// GetAcpclawHistoryDir returns the history data directory.
// Note: callers must ensure the directory exists (use os.MkdirAll).
func GetAcpclawHistoryDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "history")
}

// GetAcpclawCronDir returns the cron data directory.
// Note: callers must ensure the directory exists (use os.MkdirAll).
func GetAcpclawCronDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "cron")
}
