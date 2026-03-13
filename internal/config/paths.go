package config

import (
	"fmt"
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

// GetAcpclawContextDir returns the context file directory (same as BaseDir).
// Note: callers must ensure the directory exists (use os.MkdirAll).
func GetAcpclawContextDir() string {
	return GetAcpclawBaseDir()
}

// EnsureAcpclawMemoryDir ensures the memory directory exists and returns the path.
func EnsureAcpclawMemoryDir() (string, error) {
	dir := GetAcpclawMemoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create memory dir: %w", err)
	}
	return dir, nil
}

// EnsureAcpclawHistoryDir ensures the history directory exists and returns the path.
func EnsureAcpclawHistoryDir() (string, error) {
	dir := GetAcpclawHistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create history dir: %w", err)
	}
	return dir, nil
}

// EnsureAcpclawCronDir ensures the cron directory exists and returns the path.
func EnsureAcpclawCronDir() (string, error) {
	dir := GetAcpclawCronDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create cron dir: %w", err)
	}
	return dir, nil
}
