package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhu327/acpclaw/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, 30, cfg.Agent.ConnectTimeout)
	assert.Equal(t, config.DefaultMaxQueued, cfg.Agent.PromptQueue.MaxQueued)
	assert.Equal(t, "ask", cfg.Permissions.Mode)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "text", cfg.Logging.Format)
}

func TestLoad_FromYAML(t *testing.T) {
	yaml := `
telegram:
  token: "test-token"
  allowed_user_ids: [123, 456]
  allowed_usernames: ["alice", "bob"]
agent:
  command: "codex"
  workspace: "/tmp"
  connect_timeout: 60
`
	f := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(f, []byte(yaml), 0o600))

	cfg, err := config.Load(f)
	require.NoError(t, err)
	assert.Equal(t, "test-token", cfg.Telegram.Token)
	assert.Equal(t, []int64{123, 456}, cfg.Telegram.AllowedUserIDs)
	assert.Equal(t, []string{"alice", "bob"}, cfg.Telegram.AllowedUsernames)
	assert.Equal(t, "codex", cfg.Agent.Command)
	assert.Equal(t, "/tmp", cfg.Agent.Workspace)
	assert.Equal(t, 60, cfg.Agent.ConnectTimeout)
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token")
	t.Setenv("ACP_AGENT_COMMAND", "claude")
	t.Setenv("ACP_PERMISSION_MODE", "approve")
	t.Setenv("ACP_CONNECT_TIMEOUT", "45")

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, "env-token", cfg.Telegram.Token)
	assert.Equal(t, "claude", cfg.Agent.Command)
	assert.Equal(t, "approve", cfg.Permissions.Mode)
	assert.Equal(t, 45, cfg.Agent.ConnectTimeout)
}

func TestLoad_AllowedUsersFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "111,222,333")
	t.Setenv("TELEGRAM_ALLOWED_USERNAMES", "alice,bob")

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, []int64{111, 222, 333}, cfg.Telegram.AllowedUserIDs)
	assert.Equal(t, []string{"alice", "bob"}, cfg.Telegram.AllowedUsernames)
}

func TestLoad_InvalidAllowedUsersFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "111,not-a-number")

	_, err := config.Load("")
	require.Error(t, err)
	assert.ErrorContains(t, err, "TELEGRAM_ALLOWED_USER_IDS")
}

func TestValidate_MissingToken(t *testing.T) {
	cfg := &config.Config{}
	err := cfg.Validate()
	assert.ErrorContains(t, err, "telegram channel must be configured")
}

func TestValidate_TelegramEnabledMissingToken(t *testing.T) {
	cfg := &config.Config{
		Telegram: config.TelegramConfig{Enabled: true},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "telegram is enabled but token is missing")
}

func TestValidate_MissingCommand(t *testing.T) {
	cfg := &config.Config{Telegram: config.TelegramConfig{Enabled: true, Token: "tok"}}
	err := cfg.Validate()
	assert.ErrorContains(t, err, "agent command")
}

func TestValidate_Valid(t *testing.T) {
	cfg := &config.Config{
		Telegram:    config.TelegramConfig{Enabled: true, Token: "tok"},
		Agent:       config.AgentConfig{Command: "codex"},
		Permissions: config.PermissionsConfig{Mode: "ask"},
	}
	assert.NoError(t, cfg.Validate())
}

func TestValidate_InvalidPermissionMode(t *testing.T) {
	cfg := &config.Config{
		Telegram:    config.TelegramConfig{Enabled: true, Token: "tok"},
		Agent:       config.AgentConfig{Command: "codex"},
		Permissions: config.PermissionsConfig{Mode: "typo"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid permission mode")
}

func TestValidate_TrimmedEmptyValues(t *testing.T) {
	cfg := &config.Config{
		Telegram: config.TelegramConfig{Enabled: true, Token: "   "},
		Agent:    config.AgentConfig{Command: "   "},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "telegram is enabled but token is missing")
}

func TestConfig_PermissionEventOutputDefault(t *testing.T) {
	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, "stdout", cfg.Permissions.EventOutput)
}

func TestConfig_PermissionEventOutputEnvOverride(t *testing.T) {
	t.Setenv("ACP_PERMISSION_EVENT_OUTPUT", "off")

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, "off", cfg.Permissions.EventOutput)
}

func TestConfig_ValidatePermissionEventOutput(t *testing.T) {
	cfg := &config.Config{
		Telegram:    config.TelegramConfig{Enabled: true, Token: "tok"},
		Agent:       config.AgentConfig{Command: "codex"},
		Permissions: config.PermissionsConfig{Mode: "ask", EventOutput: "invalid"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid permission event_output")
}
