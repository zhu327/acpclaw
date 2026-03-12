package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetAcpclawBaseDir 返回 acpclaw 的基础目录 (~/.acpclaw)
// 如果无法获取用户主目录，使用系统临时目录作为降级方案
func GetAcpclawBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to temp directory if HOME is not available
		return filepath.Join(os.TempDir(), ".acpclaw")
	}
	return filepath.Join(home, ".acpclaw")
}

// GetAcpclawMemoryDir 返回 memory 数据目录
// 注意：调用者需要确保目录存在（使用 os.MkdirAll）
func GetAcpclawMemoryDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "memory")
}

// GetAcpclawHistoryDir 返回 history 数据目录
// 注意：调用者需要确保目录存在（使用 os.MkdirAll）
func GetAcpclawHistoryDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "history")
}

// GetAcpclawCronDir 返回 cron 数据目录
// 注意：调用者需要确保目录存在（使用 os.MkdirAll）
func GetAcpclawCronDir() string {
	return filepath.Join(GetAcpclawBaseDir(), "cron")
}

// GetAcpclawContextDir 返回 context 文件目录（与 BaseDir 相同）
// 注意：调用者需要确保目录存在（使用 os.MkdirAll）
func GetAcpclawContextDir() string {
	return GetAcpclawBaseDir()
}

// EnsureAcpclawMemoryDir 确保 memory 目录存在并返回路径
func EnsureAcpclawMemoryDir() (string, error) {
	dir := GetAcpclawMemoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create memory dir: %w", err)
	}
	return dir, nil
}

// EnsureAcpclawHistoryDir 确保 history 目录存在并返回路径
func EnsureAcpclawHistoryDir() (string, error) {
	dir := GetAcpclawHistoryDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create history dir: %w", err)
	}
	return dir, nil
}

// EnsureAcpclawCronDir 确保 cron 目录存在并返回路径
func EnsureAcpclawCronDir() (string, error) {
	dir := GetAcpclawCronDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create cron dir: %w", err)
	}
	return dir, nil
}
