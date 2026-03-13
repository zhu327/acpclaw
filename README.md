# acpclaw

A Telegram bot that connects users to AI agents via the [Agent Client Protocol (ACP)](https://agentclientprotocol.com/). Built in Go with support for agent memory, scheduled tasks (cron), and multiple concurrent sessions.

## Features

- **Multi-session support** — Create, resume, and manage multiple chat sessions with different workspaces
- **Agent memory system** — Persistent memory with categories (identity, preferences, people, projects, notes)
- **Automatic summarization** — Session transcripts are auto-summarized and stored as episodes
- **Scheduled tasks** — Create cron jobs that trigger at specific times (requires agent tool support)
- **MCP integration** — Exposes memory and cron tools to agents via Model Context Protocol
- **Permission modes** — Flexible permission system (ask/approve/deny) for tool execution
- **Telegram proxy support** — Works with proxy servers for Telegram API access

## Architecture

```
Telegram User ↔ acpclaw (main bot) ↔ ACP Agent (subprocess)
                                         ↓
                              MCP Server (mcp binary)
                              - Memory tools
                              - Cron tools
```

## Binaries

- `acpclaw` — Main Telegram bot and session orchestrator
- `mcp` — MCP stdio server exposing memory and cron tools to agents

## Quick Start

### 1. Installation

```bash
make build           # Builds acpclaw and mcp binaries
make install         # Installs to $GOPATH/bin
```

### 2. Configuration

Create `config.yaml` in the directory where you run the bot (or use environment variables):

```yaml
telegram:
  token: "YOUR_BOT_TOKEN"           # or TELEGRAM_BOT_TOKEN env var
  allowed_user_ids: []              # empty = allow all users
  allowed_usernames: []             # optional: restrict by username
  proxy: ""                         # optional: socks5://host:port or http://host:port

agent:
  command: "claude"                 # or ACP_AGENT_COMMAND env var (required)
  workspace: "."                    # default workspace for new sessions
  connect_timeout: 30               # seconds to wait for agent handshake

permissions:
  mode: "ask"                       # ask | approve | deny (default: ask)
  event_output: ""                  # stdout | off (default: off)

memory:
  enabled: true                     # enable memory system (default: false)
  auto_summarize: true              # auto-summarize session transcripts (default: false)
  first_prompt_context: true        # include memory in first prompt (default: false)

cron:
  enabled: true                     # enable cron/scheduled tasks (default: false)

logging:
  level: "info"                     # debug | info | warn | error (default: info)
  format: "text"                    # text | json (default: text)
```

### 3. Run

```bash
./acpclaw -config config.yaml
```

Or use defaults (looks for `config.yaml` in current dir, falls back to env vars):
```bash
./acpclaw
```

### Testing

Use echo mode to test without a real agent:
```bash
./acpclaw -echo
```

## Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/help` | Show available commands |
| `/new [workspace]` | Start a new session (optional: specify workspace path) |
| `/session` | List all sessions for this chat |
| `/resume [N]` | Resume session N (number from `/session` list) |
| `/cancel` | Cancel current prompt execution |
| `/reconnect` | Reconnect to the ACP agent subprocess |
| `/status` | Show current status and active session |

## Environment Variables

All config.yaml settings can be overridden with environment variables:

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (required) |
| `ACP_AGENT_COMMAND` | Agent command to spawn (required) |
| `ACP_AGENT_WORKSPACE` | Default workspace (default: ".") |
| `ACP_CONNECT_TIMEOUT` | Agent handshake timeout in seconds (default: 30) |
| `ACP_PERMISSION_MODE` | ask / approve / deny (default: ask) |
| `ACP_PERMISSION_EVENT_OUTPUT` | stdout / off (default: off) |
| `ACP_LOG_LEVEL` | debug / info / warn / error (default: info) |
| `ACP_LOG_FORMAT` | text / json (default: text) |
| `ACP_MEMORY_ENABLED` | true / false (default: false) |
| `ACP_MEMORY_AUTO_SUMMARIZE` | true / false (default: false) |
| `ACP_MEMORY_FIRST_PROMPT_CONTEXT` | true / false (default: false) |
| `ACP_CRON_ENABLED` | true / false (default: false) |
| `ACP_TELEGRAM_PROXY` | Proxy URL for Telegram API (e.g., socks5://host:port) |

## Memory System

When enabled, acpclaw maintains a persistent memory database in `~/.acpclaw/`:

**Memory categories:**

- **SOUL.md** — Personality, values, and communication style (identity)
- **knowledge/owner-profile.md** — Personal background and information
- **knowledge/preferences.md** — Habits, tools, workflow preferences
- **knowledge/people.md** — Contacts and relationships
- **knowledge/projects.md** — Project notes and technical decisions
- **knowledge/notes.md** — General miscellaneous knowledge

**Features:**

- Memory is automatically indexed in SQLite for full-text search
- Sessions are auto-summarized (if enabled) and stored as episodes
- Memory context is optionally prepended to the first prompt
- Agents can read, search, and save memory via MCP tools

**Directory structure:**
```
~/.acpclaw/
├── SOUL.md                  # Identity/personality
├── knowledge/               # Knowledge base
│   ├── owner-profile.md
│   ├── preferences.md
│   ├── people.md
│   ├── projects.md
│   └── notes.md
├── episodes/                # Auto-summarized sessions (one file per session)
├── history/                 # Chat history (SQLite)
└── cron/                    # Scheduled tasks (SQLite)
```

## Cron/Scheduled Tasks

When enabled, agents can create scheduled tasks via MCP tools:

- **cron_create** — Schedule a message/prompt at specific times (cron expression)
- **cron_list** — List all scheduled tasks
- **cron_run** — Trigger a task immediately
- **cron_delete** — Remove a scheduled task

Example cron expressions:
- `0 9 * * 1-5` — Every weekday at 9 AM
- `0 */4 * * *` — Every 4 hours
- `30 2 * * 0` — Every Sunday at 2:30 AM

## Build

```bash
make build           # Build acpclaw and mcp binaries
make test            # Run tests with race detector
make lint            # Run linter
make fmt             # Format code
make vet             # Run go vet
make install         # Build and install to $GOPATH/bin
make clean           # Remove binaries and coverage files
```

## Project Structure

```
acpclaw/
├── cmd/
│   ├── acpclaw/          # Main bot entrypoint
│   └── mcp/              # MCP server entrypoint
├── internal/
│   ├── agent/            # ACP agent client and session management
│   ├── channel/telegram/ # Telegram channel implementation
│   ├── config/           # Configuration loading and validation
│   ├── cron/             # Scheduled task management
│   ├── dispatcher/       # Command and message routing
│   ├── domain/           # Interfaces and domain models
│   ├── memory/           # Memory service (knowledge base, history)
│   ├── acpclient/        # ACP protocol client wrapper
│   ├── mcp/              # MCP tool implementations
│   └── templates/        # Embedded memory templates
└── Makefile
```

## Development

```bash
# Run with debug logging
ACP_LOG_LEVEL=debug ACP_LOG_FORMAT=json ./acpclaw

# Test with echo mode (no real agent)
./acpclaw -echo

# Run all tests
make test

# Format and lint
make fmt lint
```

## License

MIT License — see [LICENSE](LICENSE)
