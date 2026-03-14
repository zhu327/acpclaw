package commands

import "strings"

func resolveWorkspace(args []string, defaultWs string) string {
	ws := strings.TrimSpace(strings.Join(args, " "))
	if ws == "" {
		if defaultWs != "" {
			return defaultWs
		}
		return "."
	}
	return ws
}
