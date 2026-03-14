# acpclaw

Turn any [ACP (Agent Client Protocol)](https://agentclientprotocol.com/) compatible coding agent into your personal assistant — chat with AI agents like Claude Code through Telegram, anytime, anywhere. What was once locked inside your terminal becomes a always-on assistant with persistent memory and scheduled tasks.

## Why acpclaw?

Coding agents like Claude Code and Cursor are incredibly powerful, but they're trapped in terminals and IDEs. acpclaw sets them free:

- **Always reachable** — Talk to your agent on the go via Telegram, no laptop required
- **Persistent memory** — The agent remembers your preferences, project context, and contacts across sessions
- **Scheduled tasks** — Let the agent run tasks on a schedule (monitoring, reminders, data collection)
- **Multi-session** — Maintain sessions across multiple workspaces, switch anytime

## How It Works

```
Telegram ↔ acpclaw ↔ ACP Agent (e.g. Claude Code)
                         ↓
              Built-in MCP Server
              - Memory tools
              - Cron tools
```

acpclaw sits between Telegram and the ACP agent subprocess. It manages the agent lifecycle via ACP, and exposes memory and cron tools to the agent via MCP — enabling the agent to autonomously read/write memories and create scheduled tasks.

## Quick Start

### 1. Install

```bash
go install github.com/zhu327/acpclaw/cmd/acpclaw@latest
```

Or build from source:

```bash
git clone https://github.com/zhu327/acpclaw.git
cd acpclaw
make build    # produces the acpclaw binary
make install  # installs to $GOPATH/bin
```

### 2. Configure

Create `config.yaml`:

```yaml
telegram:
  token: "YOUR_BOT_TOKEN"           # or set TELEGRAM_BOT_TOKEN env var
  allowed_user_ids: []              # empty = allow all users
  allowed_usernames: []             # optional: restrict by username
  proxy: ""                         # optional: socks5://host:port or http://host:port

agent:
  command: "claude"                 # any ACP-compatible agent (claude, aider, etc.)
  workspace: "."                    # default working directory
  connect_timeout: 30               # agent handshake timeout in seconds

permissions:
  mode: "ask"                       # ask | approve | deny
  event_output: ""                  # stdout | off

memory:
  enabled: true                     # enable persistent memory
  auto_summarize: true              # auto-summarize sessions into episodes
  first_prompt_context: true        # inject memory context into first prompt

cron:
  enabled: true                     # enable scheduled tasks

logging:
  level: "info"                     # debug | info | warn | error
  format: "text"                    # text | json
```

### 3. Run

```bash
./acpclaw -config config.yaml
```

Or use environment variables (suitable for containerized deployments):

```bash
export TELEGRAM_BOT_TOKEN="your-token"
export ACP_AGENT_COMMAND="claude"
./acpclaw
```

Test without a real agent:

```bash
./acpclaw -echo
```

## Telegram Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/help` | Show all commands |
| `/new [workspace]` | Start a new session (optionally specify working directory) |
| `/session` | List all sessions |
| `/resume [N]` | Resume session N |
| `/cancel` | Cancel the current running task |
| `/reconnect` | Reconnect the agent subprocess |
| `/status` | Show current status |

## Memory System

When enabled, acpclaw maintains a persistent memory store in `~/.acpclaw/`, giving the agent long-term knowledge about you:

```
~/.acpclaw/
├── SOUL.md                  # Identity and personality
├── knowledge/               # Knowledge base
│   ├── owner-profile.md     # Your personal background
│   ├── preferences.md       # Habits and workflow preferences
│   ├── people.md            # Contacts and relationships
│   ├── projects.md          # Project notes and technical decisions
│   └── notes.md             # General notes
├── episodes/                # Auto-summarized session transcripts
├── history/                 # Chat history (SQLite)
└── cron/                    # Scheduled tasks (SQLite)
```

- Memory is indexed in SQLite for full-text search
- Sessions are auto-summarized and stored as episodes
- Memory context is optionally injected into the first prompt so the agent knows you from the start
- The agent reads, searches, and writes memory autonomously via MCP tools

## Scheduled Tasks

The agent can create and manage scheduled tasks via MCP tools:

| MCP Tool | Description |
|----------|-------------|
| `cron_create` | Schedule a task with a cron expression |
| `cron_list` | List all tasks |
| `cron_run` | Trigger a task immediately |
| `cron_delete` | Delete a task |

Example: `0 9 * * 1-5` — every weekday at 9 AM

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (required) |
| `ACP_AGENT_COMMAND` | Agent command to spawn (required) |
| `ACP_AGENT_WORKSPACE` | Default workspace (default: `.`) |
| `ACP_CONNECT_TIMEOUT` | Handshake timeout in seconds (default: 30) |
| `ACP_PERMISSION_MODE` | ask / approve / deny (default: ask) |
| `ACP_PERMISSION_EVENT_OUTPUT` | stdout / off |
| `ACP_LOG_LEVEL` | debug / info / warn / error (default: info) |
| `ACP_LOG_FORMAT` | text / json (default: text) |
| `ACPCLAW_MEMORY_ENABLED` | true / false (default: false) |
| `ACPCLAW_FIRST_PROMPT_CONTEXT` | true / false (default: false) |
| `ACPCLAW_CRON_ENABLED` | true / false (default: false) |
| `TELEGRAM_PROXY` | Proxy URL for Telegram API |

## Project Structure

```
acpclaw/
├── cmd/acpclaw/                 # Entrypoint
├── internal/
│   ├── domain/                  # Domain interfaces and models
│   ├── framework/               # Plugin framework (lifecycle pipeline, hook registry)
│   ├── builtin/                 # Built-in plugin implementation
│   │   ├── agent/               # ACP agent client and session management
│   │   ├── channel/telegram/    # Telegram channel adapter
│   │   ├── commands/            # Slash commands (/new, /resume, etc.)
│   │   ├── memory/              # Memory service (knowledge base, history)
│   │   ├── cron/                # Cron scheduler
│   │   └── mcp/                 # MCP tool implementations
│   ├── acpclient/               # ACP protocol client wrapper
│   ├── config/                  # Configuration loading
│   └── templates/               # Built-in memory templates
├── Makefile
└── go.mod
```

## Development

```bash
make build       # Build binary
make test        # Run tests with race detector
make lint        # Lint
make fmt         # Format
make clean       # Clean build artifacts
```

## License

MIT License — see [LICENSE](LICENSE)
