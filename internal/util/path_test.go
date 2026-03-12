package util_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zhu327/acpclaw/internal/util"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde with path",
			input:    "~/.acpclaw/memory",
			expected: filepath.Join(home, ".acpclaw/memory"),
		},
		{
			name:     "tilde only",
			input:    "~",
			expected: home,
		},
		{
			name:     "absolute path",
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name:     "relative path with dot",
			input:    "./relative",
			expected: filepath.Join(cwd, "relative"),
		},
		{
			name:     "relative path without dot",
			input:    "relative",
			expected: filepath.Join(cwd, "relative"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := util.ExpandPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExpandPath_AbsolutePaths(t *testing.T) {
	tests := []string{
		"~/.acpclaw/memory",
		"~/test",
		"./relative",
		"relative",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			result := util.ExpandPath(input)
			if input != "" {
				assert.True(t, filepath.IsAbs(result), "result should be absolute path")
			}
		})
	}
}
