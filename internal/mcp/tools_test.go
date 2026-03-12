package mcp

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePathWithinBase_AllowsPathInsideBase(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "nested", "file.txt")

	got, err := validatePathWithinBase(target, base)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(target), got)
}

func TestValidatePathWithinBase_RejectsRelativePath(t *testing.T) {
	base := t.TempDir()

	_, err := validatePathWithinBase("relative/file.txt", base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestValidatePathWithinBase_RejectsPathOutsideBase(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(filepath.Dir(base), "outside.txt")

	_, err := validatePathWithinBase(outside, base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside allowed base directory")
}
