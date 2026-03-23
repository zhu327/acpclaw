# acpclaw

Turn any [ACP (Agent Client Protocol)](https://agentclientprotocol.com/) compatible coding agent into your personal assistant — chat with AI agents through messaging apps like Telegram or WeChat, anytime, anywhere. What was once locked inside your terminal becomes an always-on assistant with persistent memory and scheduled tasks.

## Why acpclaw?

Coding agents like Claude Code and OpenCode are incredibly powerful, but they're trapped in terminals and IDEs. acpclaw sets them free:

- **Always reachable** — Talk to your agent on the go via Telegram or WeChat, no laptop required
- **Persistent memory** — The agent remembers your preferences, project context, and contacts across sessions
- **Multi-channel** — Connect via Telegram or WeChat; both channels can run simultaneously
- **Multi-session** — Maintain sessions across multiple workspaces, switch anytime

## How It Works

```
Telegram / WeChat ↔ acpclaw ↔ ACP Agent (e.g. opencode, cursor agent cli)
                                    ↓
                           Built-in MCP Server
                           - Memory tools
                           - Cron tools
```

acpclaw sits between the messaging channel and the ACP agent subprocess. It manages the agent lifecycle via ACP, and exposes memory tools to the agent via MCP — enabling the agent to autonomously read/write memories across sessions.

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

Copy `config.example.yaml` to `config.yaml` (or create from scratch). The repo’s `.gitignore` excludes `config.yaml` so local secrets are not committed by default.

Example `config.yaml`:

```yaml
telegram:
  enabled: true                     # set true to enable Telegram channel
  token: "YOUR_BOT_TOKEN"           # Telegram bot token
  allowed_user_ids: []              # restrict by user ID (empty = allow all)
  proxy: ""                         # optional: socks5://host:port or http://host:port

weixin:
  enabled: false                    # set true to enable WeChat channel
  token_path: ""                    # optional: credentials file path

agent:
  command: "opencode acp"           # any ACP-compatible agent command
  workspace: "./workspace"          # default working directory for the agent
  connect_timeout: 30               # agent handshake timeout in seconds
  model: "your-model-id"            # model to use (passed to the agent)
  prompt_queue:
    max_queued: 5                   # max prompts waiting per chat (FIFO; full queue rejects with a hint)
    # Idle worker reclaim: after the queue stays empty this long, the per-chat worker exits (frees map entry + goroutine).
    # Use -1 to disable reclaim (workers never exit; map can grow with many chats — not recommended for public bots).
    idle_timeout_seconds: 300
    # Per-prompt wall time; 0 disables. On deadline, acpclaw calls prompter.Cancel once; the agent must honor context/cancel.
    job_timeout_seconds: 600

permissions:
  mode: "approve"                   # ask | approve | deny
  event_output: "stdout"            # stdout | off

memory:
  enabled: true                     # enable persistent memory
  first_prompt_context: true        # inject memory context into first prompt

logging:
  level: "info"                     # debug | info | warn | error
  format: "text"                    # text | json
```

### 3. Run

```bash
./acpclaw -config config.yaml
```

Test without a real agent:

```bash
./acpclaw -echo
```

## Channels

### Telegram

Set `telegram.enabled: true` and provide a bot token. Optionally restrict access via `allowed_user_ids` and configure a proxy if needed.

```yaml
telegram:
  enabled: true
  token: "YOUR_BOT_TOKEN"
  allowed_user_ids: []        # restrict by user ID (empty = allow all)
  proxy: ""                   # optional: socks5://host:port or http://host:port
```

### WeChat (微信)

Set `weixin.enabled: true`. On first launch, a QR code is printed to the terminal — scan it with WeChat to log in. Credentials are cached so subsequent restarts skip the QR step.

```yaml
weixin:
  enabled: true
  token_path: ""    # optional: path to credentials file (default: ~/.acpclaw/weixin-bot/credentials.json)
```

**Important: enable `approve` mode for permissions.**

WeChat does not support inline buttons or rich UI, so the agent cannot interactively ask for tool-use approval mid-conversation. Set `permissions.mode: "approve"` to automatically approve all tool calls, otherwise the agent will stall waiting for a permission UI that WeChat cannot display:

```yaml
permissions:
  mode: "approve"     # required for WeChat — "ask" mode blocks indefinitely
  event_output: "stdout"
```

> Both Telegram and WeChat can be enabled simultaneously. Each channel runs independently and shares the same agent and memory backend.

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
└── history/                 # Chat history (SQLite)
```

- Memory is indexed in SQLite for full-text search
- Memory context is optionally injected into the first prompt so the agent knows you from the start
- The agent reads, searches, and writes memory autonomously via MCP tools

## Agent Workspace

The `agent.workspace` config sets the default working directory the agent operates in. You can point this to any directory — the agent will use it as its root for reading files, running commands, and accessing project context.

### Example Workspace

This repo ships a ready-to-use example workspace at `./workspace/`. It is pre-configured with:

- **`.cursor/mcp.json`** — Registers `acpclaw mcp` as an MCP server so the agent has access to memory tools out of the box
- **`.agents/skills/`** — A collection of agent skills the agent can draw on:
  - `weather/` — Get current weather and forecasts via wttr.in (no API key needed)
  - `summarize/` — Summarize URLs, YouTube videos, and local files via the `summarize` CLI
  - `automation-workflows/` — Design and implement automation workflows
  - `skill-creator/` — Create, evaluate, and iteratively improve new agent skills

You can use this workspace as-is as your assistant's working directory, or use it as a starting point and add your own skills and configuration:

```yaml
agent:
  workspace: "./workspace"
```

The MCP configuration in `workspace/.cursor/mcp.json` automatically connects the agent to acpclaw's built-in memory server:

```json
{
  "mcpServers": {
    "acpclaw-memory": {
      "command": "/usr/local/bin/acpclaw",
      "args": ["mcp"]
    }
  }
}
```

Make sure the `acpclaw` binary is installed at the path specified (or adjust the path to match your installation).

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
│   │   ├── channel/weixin/      # WeChat channel adapter
│   │   ├── commands/            # Slash commands (/new, /resume, etc.)
│   │   ├── memory/              # Memory service (knowledge base, history)
│   │   └── mcp/                 # MCP tool implementations
│   ├── acpclient/               # ACP protocol client wrapper
│   ├── config/                  # Configuration loading
│   └── templates/               # Built-in memory templates
├── workspace/                   # Example agent workspace (see above)
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
