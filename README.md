# 🛸 Antigravity Go Telegram Bot Agent

[![CI](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/Coverage-75.4%25-brightgreen.svg)](https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent/actions)
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
| [`handlers.go`](handlers.go) | Telegram update router, slash-command handlers (`/stop`, `/export`, etc.), interactive callback queries (`cmd:stop`, `cmd:retry`), and media downloads with `.md` drop-to-resume. |
| [`account_pool.go`](account_pool.go) | Multi-account isolation, dynamic quota monitoring, automatic cooldown backoff & auto-recovery, and cache deduplication. |
| [`account_handlers.go`](account_handlers.go) | Account management handlers, status dashboards, manual account switching, and `/accounts` command. |
| [`session.go`](session.go) | `AgySession` process lifecycle, mutex-decoupled non-blocking I/O, 1200ms streaming throttler, Inactivity Turn Watchdog, Stream Auto-Recovery, and 429 Quota Safe Parking. |
| [`subprocess_watchdog.go`](subprocess_watchdog.go) | Autonomous `/proc` scanner and reaper eliminating `SIGTTIN`/`SIGTTOU` state `T` deadlocks via two-phase `SIGCONT` + `SIGKILL`. |
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
| `/start` | None | Displays live Agent Terminal dashboard (CWD, model, active account, session uptime, steps count, quick action keyboard). |
| `/model` | None | Opens interactive inline keyboard to switch the active LLM model with seamless Hot Model Swap (100% context retention). |
| `/refresh_models`| None | Dynamically fetches the latest model list from `agy --print /models`. |
| `/accounts` | `[status\|switch\|check]` | Multi-account quota pool dashboard, active account switching, and live quota health checks. |
| `/usage` | None | Queries and displays current token quota and tier usage. |
| `/clear` | None | Resets session context, terminates background tasks, and issues a fresh conversation UUID. |
| `/resume` | None | Presents an interactive picker of previous sessions sorted by last modification time. |
| `/export` | None | Compiles and sends full conversation transcript as a clean Markdown document. |
| `/rename` | `<name>` | Renames the current session in brain storage (`.title`). |
| `/workspace` | `<path>` | Switches working directory (sandboxed under `AGENTS_DIR` with symlink traversal checks). |
| `/voice` | `[on\|off]`| Toggles persistent voice responses generated via ElevenLabs TTS. |
| `/tts` | `<text>` | Synthesizes arbitrary text into speech and sends as a voice note. |
| `/stop` (or `/cancel`) | None | Gracefully interrupts active execution turn, terminates subprocess process group (`SIGTERM`/`SIGKILL`), salvages output buffer, and preserves conversation context. |
| `/help` | None | Displays comprehensive command reference. |
| `/grill_me` (or `/grill-me`) | None | Triggers interactive interview slash-command in Antigravity CLI (auto-aliased for Telegram command syntax). |
| `/teamwork_preview` (or `/teamwork-preview`) | None | Triggers multi-agent collaboration preview (auto-aliased for Telegram command syntax). |

---

## 🛡️ Autonomous Resilience & Fault-Tolerance

The engine features an enterprise-grade resilience suite engineered for 24/7 headless production:

* **Inactivity Turn Watchdog (`15m` inactivity, `45m` hard deadline)**: Monitors `LastActivity` updated on all JSONL step events. If an agent hangs, stalls, or deadlocks without activity for 15 minutes, or exceeds the 45-minute hard deadline, the watchdog terminates the rogue process group via `s.Kill()`, salvages all accumulated output text, and delivers it to Telegram with full artifact extraction.
* **Two-Phase Process Group Annihilation (`Setpgid: true`)**: Child processes run in isolated kernel process groups. Interruption (`/stop` or watchdog) sends `SIGTERM` followed by `SIGKILL` to `-pgid`, eliminating all child compiler, worker, and PTY processes without zombies.
* **Asynchronous Coalescing Throttler (`1200ms`)**: Buffers rapid token streams and flushes edits at 1.2-second intervals, eliminating Telegram `429 Too Many Requests` deadlocks and empty message race conditions.
* **Stream Auto-Recovery with Buffer Salvaging (Up to 2 Retries)**: Automatically recovers and reconnects when underlying Google Cloud streaming sockets are severed mid-turn, preserving existing output buffers and continuing the task seamlessly without user intervention.
* **Cross-Turn 429 Safe Parking Protocol**: When upstream model quotas are exhausted, the engine automatically compiles the active session into a Markdown export file (`session_<title>.md`), parks the session safely, and delivers the file to the user.
* **Drop-to-Resume Workflow**: Users can forward or drop any `session_*.md` file directly into chat. The engine automatically parses the transcript, restores previous conversational context, and resumes execution from where it left off.
* **Subprocess State: T Reaper**: An autonomous `/proc` scanner detects and reaps commands suspended by `SIGTTIN`/`SIGTTOU` via sequenced `SIGCONT` + `SIGKILL`.

---

## ⚙️ Environment Variables & Configuration

The engine supports flexible configuration through environment variables:

| Variable | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `BOT_TOKENS` | String | `""` | Comma-separated list of Telegram Bot API tokens. |
| `ALLOWED_ADMIN_IDS` | String | `""` | Comma-separated list of authorized Telegram User IDs (Fail-Fast enforced at startup). |
| `TURN_INACTIVITY_TIMEOUT_MINUTES` | Integer | `15` | Maximum duration of silence allowed before the turn watchdog salvages buffer and kills process. |
| `TURN_HARD_DEADLINE_MINUTES` | Integer | `45` | Absolute maximum duration for an active turn as a runaway failsafe. |
| `AGENTS_DIR` | String | `/root/.agents` | Base directory containing agent workspaces and download scratchpads. |
| `BRAIN_DIR` | String | `/root/.gemini/antigravity-cli/brain` | Storage directory for conversation logs, titles, and steps. |
| `PROJECTS_DIR` | String | `/root/projects` | Base directory for external repository projects and safe `/workspace` boundary. |
| `DATA_DIR` | String | `data` | Directory where SQLite state databases (`sessions_<bot>.db`) are persisted. |
| `AGY_BINARY` | String | `~/.local/bin/agy` | Absolute path to the Antigravity CLI binary (defaults to `~/.local/bin/agy`). |
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
