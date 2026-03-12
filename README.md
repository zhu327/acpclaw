# telegram-acp-bot (Go)

A Telegram bot that connects users to AI agents via the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/).

## Architecture

```
Telegram User ↔ TelegramBridge ↔ AcpAgentService ↔ ACP Agent (subprocess)
                                        ↓
                             MCP Channel (telegram-channel binary)
```

## Binaries

- `telegram-acp-bot` — main Telegram bot
- `mcp-channel` — MCP stdio server exposing Telegram tools to agents

## Usage

### Configuration

Create `config.yaml` (all values can be overridden by environment variables):

```yaml
telegram:
  token: "YOUR_BOT_TOKEN"        # or TELEGRAM_BOT_TOKEN env var
  allowed_user_ids: []           # empty = allow all
  allowed_usernames: []

agent:
  command: "claude"              # or ACP_AGENT_COMMAND env var
  workspace: "."
  connect_timeout: 30

permissions:
  mode: "ask"                    # ask | approve | deny
```

### Run

```bash
./telegram-acp-bot -config config.yaml
```

### Commands

| Command | Description |
|---------|-------------|
| `/new [workspace]` | Start a new session |
| `/resume [N]` | Resume a previous session |
| `/session` | Show current workspace |
| `/cancel` | Cancel current operation |
| `/stop` | Stop session |
| `/restart` | Restart the bot process |
| `/help` | Show help |

## Build

```bash
make build   # builds telegram-acp-bot and mcp-channel
make test    # runs tests with race detector
make lint    # runs golangci-lint
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (required) |
| `ACP_AGENT_COMMAND` | Agent command (required) |
| `ACP_PERMISSION_MODE` | ask / approve / deny (default: ask) |
| `ACP_CONNECT_TIMEOUT` | Handshake timeout in seconds (default: 30) |
| `ACP_LOG_LEVEL` | debug / info / warn / error (default: info) |
| `ACP_LOG_FORMAT` | text / json (default: text) |
| `ACP_TELEGRAM_CHANNEL_ALLOW_PATH` | Set to any value to enable file path sending |
| `ACP_TELEGRAM_CHANNEL_ALLOWED_BASE_DIR` | Absolute base directory allowed for `path` reads when allow-path is enabled |

## Context File

acpclaw maintains a context file at `~/.acpclaw/last-context.json` to share the current chat context with MCP tools. This file is automatically updated when you create or load a session and allows MCP cron tools to work without explicitly specifying channel/chatId parameters.

The file contains:
- `channel`: Current channel (e.g., "telegram")
- `chatId`: Current chat ID
- `updatedAt`: Last update timestamp

**Note:** If multiple chats are active simultaneously, the file will contain the most recently active chat's context. Updates are serialized to prevent corruption, but in high-concurrency scenarios the "last" context reflects whichever chat completed its update most recently.
