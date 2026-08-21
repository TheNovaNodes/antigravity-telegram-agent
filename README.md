---
module_type: Integration/Agent Gateway
status: Active
protocol: Telegram / MCP / PTY
primary_capability: Telegram interface for Antigravity (agy) CLI and MCP servers
requires: python 3.10+, agy binary, Telegram Bot Token
works_with: Google Antigravity, AnythingLLM, SearXNG, Nextcloud, Google Jules
last_verified: 2026-08-21
---

# Antigravity Telegram Agent

**A Telegram interface that provides real-time access and persistent session control over the Google Antigravity AI coding assistant and MCP servers.**

## Status and Last Verified Date
**Status:** Active  
**Last Verified Date:** 2026-08-21  

## What it does / does not do
**What it does:**
- Proxies Telegram chat inputs to an underlying `agy` (Google Antigravity) process using Pyte and Pexpect pseudo-terminal emulators.
- Persists session state (models, effort, modes) via SQLite per user.
- Handles automated scheduling of tasks via Sentinel Autonomous Scheduler.
- Integrates with MCP servers (AnythingLLM, SearXNG, Nextcloud, Jules).
- Delivers generated artifacts directly as Telegram attachments.

**What it does not do:**
- It does not implement the AI inference or coding logic itself; it acts solely as a bridge.
- It does not host the MCP servers natively (it manages external instances).

## Why an agent would use it
It provides an easy, mobile-friendly interface for remote coding workflows, allowing users to trigger tasks, manage MCP servers, and review artifacts from their phone without needing an SSH or desktop environment.

## Architecture and dependencies
**Architecture:**
- **PTY Execution Layer:** Runs `agy` via `pexpect.spawn` paired with `pyte` virtual terminal emulator to handle ANSI escapes and streams asynchronously.
- **Session Management:** Uses SQLite (`data/antigravity-telegram-agent.db`) to persist configurations and states per Telegram user chat.
- **MCP Manager:** Tests and toggles connectivity for multiple MCP backends (Control and Data planes).
- **Formatters:** Intercepts CLI output, stripping raw ASCII/TUI noise and converting Markdown to Telegram Rich Text HTML.

**Dependencies:**
- `aiogram>=3.4.1`
- `google-antigravity`
- `python-dotenv>=1.0.1`
- `pexpect>=4.9.0`
- `pyte>=0.8.2`
- `aiohttp>=3.9.0`
- `apscheduler>=3.10.4`
- `pytest-asyncio>=1.4.0`

## Compatibility
- Python 3.10+
- Linux/Unix environments (for PTY/pexpect support)

## Quick start and health check
**Quick Start:**
---bash
git clone https://github.com/thenovanodes/antigravity-telegram-agent.git
cd antigravity-telegram-agent
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
cp .env.example .env
# Edit .env and provide your TELEGRAM_BOT_TOKEN and ALLOWED_USER_IDS
python src/main.py
---

**Health Check:**
Run `/mcp` in the Telegram bot to view health statuses. The MCP manager performs granular HTTP & Binary health verification of MCP Data and Control planes.

## Configuration and environment variables
| Variable | Description |
| -------- | ----------- |
| `TELEGRAM_BOT_TOKEN` | Your Telegram Bot Token obtained from @BotFather. |
| `ALLOWED_USER_IDS` | Comma-separated list of Telegram User IDs permitted to use the bot. |
| `LOG_LEVEL` | Application logging level (e.g., `INFO`, `DEBUG`). |
| `AGY_BINARY_PATH` | Path to the compiled `agy` binary or entrypoint script. |

## Complete MCP tool/API table with side effects
| Server Key | Name | Type | Plane | Side Effects / Actions |
| ---------- | ---- | ---- | ----- | ---------------------- |
| `anythingllm` | AnythingLLM Semantic Memory Gateway | memory | data | Reads/writes semantic memory workspaces via HTTP API. |
| `anythingllm-control` | TheNovaNodes AnythingLLM Control Plane | admin | control | Administers AnythingLLM configuration and lifecycle via local Python script. |
| `searxng` | SearXNG Web Search Gateway | search | data | Performs web searches across multiple engines via HTTP. |
| `searxng-control` | SearXNG Control Plane | admin | control | Administers SearXNG settings and container state. |
| `nextcloud` | Nextcloud User CRM Gateway | crm | data | Interacts with Nextcloud CRM data using username/app password via HTTP. |
| `nextcloud-control` | Nextcloud Admin Control Plane | admin | control | Administers Nextcloud user accounts and settings. |
| `google-jules-doctormes` | Google Jules AI Agent (Doctormes) | agent | data | Interacts with Jules agent by piping JSON-RPC payloads to binary. |
| `google-jules-novanodes` | Google Jules AI Agent (TheNovaNodes) | agent | data | Interacts with Jules agent by piping JSON-RPC payloads to binary. |

## Security model and trust boundaries
- **Authentication:** Only Telegram users defined in `ALLOWED_USER_IDS` can interact with the bot. All other messages are ignored.
- **Secrets Management:** Environment variables are used for bot tokens and IDs. `mcp_config.json` stores API keys and should be tightly access-controlled.
- **Execution:** The bot runs `agy` commands as the user running the python process.
- **CRITICAL NOTE:** Treat the "agy eligibility binary patch" as an unsupported sandbox workaround (do NOT describe it as a supported fix). This patch bypasses typical checks and should only be used in completely isolated, unsupported experimental environments.

## Tests and exact commands
To run tests using `pytest` and `pytest-asyncio`:
---bash
python -m pytest tests/
---
(A Docker environment `Dockerfile.test_runner` is also available for isolated testing: `docker build -t agy-tests -f Dockerfile.test_runner . && docker run --rm agy-tests python -m pytest tests/`)

## Operations, logs, backup/restore, rollback
- **Operations:** Use the provided `setup.sh` to configure and install the bot as a `systemd` service (`antigravity-telegram-agent.service`).
- **Logs:** Logs can be accessed via systemd (`journalctl -u antigravity-telegram-agent -f`) or output to the console based on `LOG_LEVEL`.
- **Backup/Restore:** Back up the `.env` file, `mcp_config.json`, and the SQLite database (`data/antigravity-telegram-agent.db` if created in data dir) to save sessions. Restore by dropping them back into the directory.
- **Rollback:** Revert via `git checkout <commit_hash> && pip install -e .` and restart the systemd service.

## Generic MCP-client example
Example JSON-RPC payload sent to MCP binary agents:
---json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2024-11-05",
    "capabilities": {},
    "clientInfo": {"name": "HealthCheck", "version": "1.0.0"}
  }
}
---

## Limitations and roadmap
**Limitations:**
- Reliant on Telegram API limits (message lengths chunked at 3800 characters).
- Requires a functional `agy` CLI installed locally.
- PTY integration can sometimes drop extremely complex ANSI formatting.

**Roadmap:**
- Support for richer inline keyboards and interactive form inputs.
- Enhanced multi-user isolation within a single bot instance.

## Related TheNovaNodes modules
- AnythingLLM Semantic Memory Gateway
- SearXNG Web Search Gateway
- Google Jules MCP modules

## License
MIT License
