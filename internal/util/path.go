package util

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expands ~ to home directory and converts relative paths to absolute.
// It handles the following cases:
//   - ~/path  -> /home/user/path
//   - ~       -> /home/user
//   - ./path  -> /current/dir/path
//   - path    -> /current/dir/path
//   - /path   -> /path (unchanged)
func ExpandPath(path string) string {
	if path == "" {
		return ""
	}

	// Expand ~ or ~/
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = home
		}
	}

	// Convert to absolute path if relative
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}

	return path
}
