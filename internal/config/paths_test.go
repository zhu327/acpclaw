package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetAcpclawBaseDir(t *testing.T) {
	baseDir := GetAcpclawBaseDir()

	// Verify it contains .acpclaw.
	if !strings.HasSuffix(baseDir, ".acpclaw") {
		t.Errorf("expected baseDir to end with .acpclaw, got: %s", baseDir)
	}

	// Verify it is an absolute path.
	if !filepath.IsAbs(baseDir) {
		t.Errorf("expected absolute path, got: %s", baseDir)
	}
}

func TestGetAcpclawMemoryDir(t *testing.T) {
	memoryDir := GetAcpclawMemoryDir()
	baseDir := GetAcpclawBaseDir()

	expected := filepath.Join(baseDir, "memory")
	if memoryDir != expected {
		t.Errorf("expected %s, got %s", expected, memoryDir)
	}
}

func TestGetAcpclawHistoryDir(t *testing.T) {
	historyDir := GetAcpclawHistoryDir()
	baseDir := GetAcpclawBaseDir()

	expected := filepath.Join(baseDir, "history")
	if historyDir != expected {
		t.Errorf("expected %s, got %s", expected, historyDir)
	}
}

func TestGetAcpclawCronDir(t *testing.T) {
	cronDir := GetAcpclawCronDir()
	baseDir := GetAcpclawBaseDir()

	expected := filepath.Join(baseDir, "cron")
	if cronDir != expected {
		t.Errorf("expected %s, got %s", expected, cronDir)
	}
}

func TestGetAcpclawContextDir(t *testing.T) {
	contextDir := GetAcpclawContextDir()
	baseDir := GetAcpclawBaseDir()

	// ContextDir should match BaseDir.
	if contextDir != baseDir {
		t.Errorf("expected contextDir to equal baseDir, got %s vs %s", contextDir, baseDir)
	}
}

func TestAcpclawPathsConsistency(t *testing.T) {
	baseDir := GetAcpclawBaseDir()

	// All subdirectories should be under baseDir.
	paths := map[string]string{
		"memory":  GetAcpclawMemoryDir(),
		"history": GetAcpclawHistoryDir(),
		"cron":    GetAcpclawCronDir(),
		"context": GetAcpclawContextDir(),
	}

	for name, path := range paths {
		if !strings.HasPrefix(path, baseDir) {
			t.Errorf("%s path (%s) should be under baseDir (%s)", name, path, baseDir)
		}
	}
}

func TestAcpclawPathsUseHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	baseDir := GetAcpclawBaseDir()
	if !strings.HasPrefix(baseDir, home) {
		t.Errorf("expected baseDir to be under home directory (%s), got: %s", home, baseDir)
	}
}

func TestGetAcpclawBaseDirFallback(t *testing.T) {
	// This test can't easily mock os.UserHomeDir failure without build tags,
	// but we document the expected behavior: fallback to temp dir
	baseDir := GetAcpclawBaseDir()
	if baseDir == "" {
		t.Error("GetAcpclawBaseDir should never return empty string")
	}
}

func TestEnsureAcpclawMemoryDir(t *testing.T) {
	dir, err := EnsureAcpclawMemoryDir()
	if err != nil {
		t.Fatalf("EnsureAcpclawMemoryDir failed: %v", err)
	}
	if dir == "" {
		t.Error("EnsureAcpclawMemoryDir returned empty string")
	}

	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Errorf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}
}

func TestEnsureAcpclawHistoryDir(t *testing.T) {
	dir, err := EnsureAcpclawHistoryDir()
	if err != nil {
		t.Fatalf("EnsureAcpclawHistoryDir failed: %v", err)
	}
	if dir == "" {
		t.Error("EnsureAcpclawHistoryDir returned empty string")
	}

	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Errorf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}
}

func TestEnsureAcpclawCronDir(t *testing.T) {
	dir, err := EnsureAcpclawCronDir()
	if err != nil {
		t.Fatalf("EnsureAcpclawCronDir failed: %v", err)
	}
	if dir == "" {
		t.Error("EnsureAcpclawCronDir returned empty string")
	}

	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Errorf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}
}
