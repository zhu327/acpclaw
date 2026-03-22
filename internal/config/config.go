package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Telegram    TelegramConfig    `yaml:"telegram"`
	Agent       AgentConfig       `yaml:"agent"`
	Permissions PermissionsConfig `yaml:"permissions"`
	Logging     LoggingConfig     `yaml:"logging"`
	Memory      MemoryConfig      `yaml:"memory"`
	Cron        CronConfig        `yaml:"cron"`
}

// CronConfig holds cron system configuration.
type CronConfig struct {
	Enabled bool `yaml:"enabled"`
}

// MemoryConfig holds memory system configuration.
type MemoryConfig struct {
	Enabled            bool `yaml:"enabled"`
	FirstPromptContext bool `yaml:"first_prompt_context"`
}

// TelegramConfig holds Telegram bot configuration.
type TelegramConfig struct {
	Enabled          bool     `yaml:"enabled"`
	Token            string   `yaml:"token"`
	AllowedUserIDs   []int64  `yaml:"allowed_user_ids"`
	AllowedUsernames []string `yaml:"allowed_usernames"`
	// Proxy is the proxy URL, e.g. socks5://host:port or http://host:port
	Proxy string `yaml:"proxy"`
}

// DefaultMaxQueued is the default cap on prompts waiting behind the in-flight one per chat.
const DefaultMaxQueued = 5

// PromptQueueConfig bounds per-chat queued prompts (not yet started) behind the in-flight one.
type PromptQueueConfig struct {
	MaxQueued int `yaml:"max_queued"`
}

// AgentConfig holds ACP agent configuration.
type AgentConfig struct {
	Command        string            `yaml:"command"`
	Workspace      string            `yaml:"workspace"`
	ConnectTimeout int               `yaml:"connect_timeout"`
	Model          string            `yaml:"model"`
	PromptQueue    PromptQueueConfig `yaml:"prompt_queue"`
}

// PermissionsConfig holds permission-related configuration.
type PermissionsConfig struct {
	Mode        string `yaml:"mode"`
	EventOutput string `yaml:"event_output"`
}

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Load reads config from a YAML file (if path non-empty) then applies env overrides.
func Load(path string) (*Config, error) {
	cfg := defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	normalizePromptQueue(&cfg.Agent.PromptQueue)
	return cfg, nil
}

// Validate checks required fields and valid enum values.
func (c *Config) Validate() error {
	if c.Telegram.Enabled && strings.TrimSpace(c.Telegram.Token) == "" {
		return fmt.Errorf("telegram is enabled but token is missing (TELEGRAM_BOT_TOKEN)")
	}
	// Warn when token is present but the channel is not enabled.
	if !c.Telegram.Enabled && strings.TrimSpace(c.Telegram.Token) != "" {
		slog.Warn("telegram token is set but channel is not enabled; set TELEGRAM_ENABLED=true to activate")
	}
	if !c.Telegram.Enabled {
		return fmt.Errorf("telegram channel must be configured (TELEGRAM_ENABLED + TELEGRAM_BOT_TOKEN)")
	}
	if strings.TrimSpace(c.Agent.Command) == "" {
		return fmt.Errorf("agent command is required (ACP_AGENT_COMMAND)")
	}
	switch c.Permissions.Mode {
	case "ask", "approve", "deny":
	default:
		return fmt.Errorf("invalid permission mode %q: must be ask, approve, or deny", c.Permissions.Mode)
	}
	if c.Permissions.EventOutput != "" {
		switch c.Permissions.EventOutput {
		case "stdout", "off":
		default:
			return fmt.Errorf("invalid permission event_output %q: must be stdout or off", c.Permissions.EventOutput)
		}
	}
	normalizePromptQueue(&c.Agent.PromptQueue)
	return nil
}

func normalizePromptQueue(p *PromptQueueConfig) {
	if p.MaxQueued <= 0 {
		p.MaxQueued = DefaultMaxQueued
	}
}

func defaults() *Config {
	return &Config{
		Agent: AgentConfig{
			Workspace:      ".",
			ConnectTimeout: 30,
			PromptQueue: PromptQueueConfig{
				MaxQueued: DefaultMaxQueued,
			},
		},
		Permissions: PermissionsConfig{
			Mode:        "ask",
			EventOutput: "stdout",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		Memory: MemoryConfig{
			Enabled:            false,
			FirstPromptContext: false,
		},
		Cron: CronConfig{
			Enabled: false,
		},
	}
}

func getEnv(key string) string { return os.Getenv(key) }

func applyEnv(cfg *Config) error {
	if v := getEnv("TELEGRAM_ENABLED"); v != "" {
		cfg.Telegram.Enabled = parseBoolEnv(v)
	}
	if v := getEnv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := getEnv("TELEGRAM_ALLOWED_USER_IDS"); v != "" {
		ids, err := parseInt64List(v)
		if err != nil {
			return fmt.Errorf("parsing TELEGRAM_ALLOWED_USER_IDS: %w", err)
		}
		cfg.Telegram.AllowedUserIDs = ids
	}
	if v := getEnv("TELEGRAM_ALLOWED_USERNAMES"); v != "" {
		cfg.Telegram.AllowedUsernames = parseStringList(v)
	}
	if v := getEnv("TELEGRAM_PROXY"); v != "" {
		cfg.Telegram.Proxy = v
	}
	if v := getEnv("ACP_AGENT_COMMAND"); v != "" {
		cfg.Agent.Command = v
	}
	if v := getEnv("ACP_CONNECT_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Agent.ConnectTimeout = n
		}
	}
	if v := getEnv("ACP_PERMISSION_MODE"); v != "" {
		cfg.Permissions.Mode = v
	}
	if v := getEnv("ACP_PERMISSION_EVENT_OUTPUT"); v != "" {
		cfg.Permissions.EventOutput = v
	}
	if v := getEnv("ACP_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := getEnv("ACP_LOG_FORMAT"); v != "" {
		cfg.Logging.Format = v
	}
	if v := getEnv("ACPCLAW_MEMORY_ENABLED"); v != "" {
		cfg.Memory.Enabled = parseBoolEnv(v)
	}
	if v := getEnv("ACPCLAW_FIRST_PROMPT_CONTEXT"); v != "" {
		cfg.Memory.FirstPromptContext = parseBoolEnv(v)
	}
	if v := getEnv("ACPCLAW_CRON_ENABLED"); v != "" {
		cfg.Cron.Enabled = parseBoolEnv(v)
	}
	if v := getEnv("ACP_PROMPT_QUEUE_MAX_QUEUED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Agent.PromptQueue.MaxQueued = n
		}
	}
	return nil
}

func parseBoolEnv(v string) bool {
	return v == "1" || strings.ToLower(v) == "true"
}

func parseInt64List(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	result := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty user ID is not allowed")
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID %q", p)
		}
		result = append(result, n)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one valid user ID is required")
	}
	return result, nil
}

func parseStringList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
}
