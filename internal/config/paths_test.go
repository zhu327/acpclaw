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

func TestAcpclawPathsConsistency(t *testing.T) {
	baseDir := GetAcpclawBaseDir()

	// All subdirectories should be under baseDir.
	paths := map[string]string{
		"memory":  GetAcpclawMemoryDir(),
		"history": GetAcpclawHistoryDir(),
		"cron":    GetAcpclawCronDir(),
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
