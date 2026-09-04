# 🛸 Antigravity Go Telegram Bot Agent

[![CI](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/Coverage-82%25-brightgreen.svg)](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=for-the-badge&logo=go)

**The Ultimate, High-Performance, Deadlock-Immune Pure Go Core for the Antigravity Telegram Bot Ecosystem.**

---

## ⚡ Quick Start: 30 Seconds to Run

Get the agent gateway up and running immediately:

```bash
# 1. Clone & enter directory
git clone https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent.git
cd antigravity-go-tg-bot-agent

# 2. Setup your .env file
echo 'BOT_TOKENS="your:telegram_token"' > .env
echo 'ALLOWED_ADMIN_IDS="123456789"' >> .env

# 3. Build & Run via Makefile
make build
./bin/antigravity-bot-engine
```

For in-depth architectural details, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). For contribution guidelines, see [CONTRIBUTING.md](CONTRIBUTING.md).

---

## 🏗️ Modular Architecture Overview

The core is decomposed into distinct, focused domain modules:

| Source Module | Responsibility |
| :--- | :--- |
| [`main.go`](main.go) | Multi-bot long-polling lifecycle, signal traps, and graceful shutdown supervisor. |
| [`handlers.go`](handlers.go) | Telegram update router, slash-command handlers, interactive callback queries, and media downloads. |
| [`session.go`](session.go) | `AgySession` process lifecycle, mutex-decoupled non-blocking I/O, streaming throttler, and Smart Auto-Fallback. |
| [`storage.go`](storage.go) | SQLite schema migrations (`data/sessions_<bot>.db`), WAL mode configuration, and user CRUD. |
| [`models.go`](models.go) | Dynamic LLM discovery from `agy models` with emoji tier badges. |
| [`tts.go`](tts.go) | Mirror Protocol TTS audio engine with multi-key ElevenLabs rotation and custom base URL support. |
| [`formatters.go`](formatters.go) | Markdown-to-Telegram-HTML conversion with tag balancing and artifact parsing. |

---

## 📖 The Genesis & Engineering Philosophy

Initially, the ecosystem relied on Python-based routing scripts. As agent workflows grew autonomous, heavy stdout JSON streams caused OS pipe buffers (64KB) to overflow. Combined with synchronous Telegram API calls, this triggered cascading HTTP `429 Too Many Requests` deadlocks and zombie subprocesses.

We engineered this **Pure Go Core** from scratch to eliminate these bottlenecks. It completely decouples stdout stream reading from Telegram network dispatching via an asynchronous throttler loop, isolates child process trees with dedicated process groups (`pgid`), and utilizes SQLite with Write-Ahead Logging (`WAL`) for zero-collision concurrent transactions.

---

## 🎮 Command Reference

| Command | Arguments | Description |
| :--- | :--- | :--- |
| `/start` | None | Displays live Agent Terminal dashboard (CWD, model, session uptime, steps count, quick action keyboard). |
| `/model` | None | Opens interactive inline keyboard to switch the active LLM model with seamless Hot Model Swap (100% context retention). |
| `/refresh_models`| None | Dynamically fetches the latest model list from `agy --print /models`. |
| `/usage` | None | Queries and displays current token quota and tier usage. |
| `/clear` | None | Resets session context, terminates background tasks, and issues a fresh conversation UUID. |
| `/resume` | None | Presents an interactive picker of previous sessions sorted by last modification time. |
| `/export` | None | Compiles and sends full conversation transcript as a clean Markdown document. |
| `/rename` | `<name>` | Renames the current session in brain storage (`.title`). |
| `/workspace` | `<path>` | Switches working directory (sandboxed under `AGENTS_DIR` with symlink traversal checks). |
| `/voice` | `[on\|off]`| Toggles persistent voice responses generated via ElevenLabs TTS. |
| `/tts` | `<text>` | Synthesizes arbitrary text into speech and sends as a voice note. |
| `/help` | None | Displays comprehensive command reference. |
| `/grill-me` | None | Trigger specialized interactive interview slash-command in Antigravity CLI. |
| `/teamwork-preview` | None | Trigger multi-agent collaboration preview. |

---

## ⚙️ Environment Variables & Configuration

The engine supports flexible configuration through environment variables:

| Variable | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `BOT_TOKENS` | String | `""` | Comma-separated list of Telegram Bot API tokens. |
| `ALLOWED_ADMIN_IDS` | String | `""` | Comma-separated list of authorized Telegram User IDs. |
| `AGENTS_DIR` | String | `/root/.agents` | Base directory containing agent workspaces and download scratchpads. |
| `BRAIN_DIR` | String | `/root/.gemini/antigravity-cli/brain` | Storage directory for conversation logs, titles, and steps. |
| `AGY_BINARY` | String | `/root/.gemini/antigravity-cli/bin/agy` | Absolute path to the Antigravity CLI binary. |
| `ELEVENLABS_API_KEY` | String | `""` | Comma or newline separated list of ElevenLabs API keys (supports auto-rotation). |
| `ELEVENLABS_BASE_URL` | String | `https://api.elevenlabs.io/v1/text-to-speech` | Configurable TTS endpoint URL (used for reverse proxies and testing). |

---

## 🛠️ Makefile & Development Workflow

Standardized development targets:

```bash
make build       # Compile binary to bin/antigravity-bot-engine
make run         # Build and run the engine locally
make test        # Run unit tests
make race        # Run unit tests with Go data race detector
make coverage    # Generate coverage report and HTML summary
make fmt         # Format all Go source files via gofmt
make clean       # Remove compiled binaries and test coverage profiles
```

---

## 🧪 Testing & CI Verification

We enforce a strict **zero-data-race** policy (`go test -race`) and high test coverage:

```bash
# Run complete test suite with race detector and coverage analysis
make race
make coverage
```

---

## 🚀 Deployment (Blood Rules)

We adhere strictly to the **ПРАВИЛА КРОВИ (Blood Rules)**:
1. **NO DIRECT PUSH TO MASTER.** All changes go through feature branches and Pull Requests.
2. **NO MANUAL PATCHES IN PROD.** All updates are verified via CI and deployed through PRs.

```bash
# 1. Pull the latest master (after PR merge)
git pull origin master

# 2. Execute standardized deployment script
./scripts/deploy.sh
```

---
*Built with adrenaline, terminal wizardry, and strict CI discipline by the Trickster and ZaVLab.* 🎭⚡
