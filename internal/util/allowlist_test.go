package util

import "testing"

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name     string
		cfg      AllowlistConfig
		userID   int64
		username string
		want     bool
	}{
		{
			name:     "empty allowlist allows all",
			cfg:      AllowlistConfig{},
			userID:   123,
			username: "alice",
			want:     true,
		},
		{
			name: "user ID in allowlist",
			cfg: AllowlistConfig{
				AllowedUserIDs: []int64{123, 456},
			},
			userID:   123,
			username: "",
			want:     true,
		},
		{
			name: "user ID not in allowlist",
			cfg: AllowlistConfig{
				AllowedUserIDs: []int64{456},
			},
			userID:   123,
			username: "",
			want:     false,
		},
		{
			name: "username in allowlist (case insensitive)",
			cfg: AllowlistConfig{
				AllowedUsernames: []string{"Alice", "bob"},
			},
			userID:   999,
			username: "alice",
			want:     true,
		},
		{
			name: "username not in allowlist",
			cfg: AllowlistConfig{
				AllowedUsernames: []string{"bob"},
			},
			userID:   999,
			username: "alice",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAllowed(tt.cfg, tt.userID, tt.username); got != tt.want {
				t.Errorf("IsAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}
