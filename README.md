# 🛸 Antigravity Telegram Agent

[![CI](https://github.com/TheNovaNodes/antigravity-telegram-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/TheNovaNodes/antigravity-telegram-agent/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/Coverage-75.8%25-brightgreen.svg)](https://github.com/TheNovaNodes/antigravity-telegram-agent/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=for-the-badge&logo=go)

**The Ultimate, High-Performance, Deadlock-Immune Pure Go Core for the Antigravity Telegram Bot Ecosystem.**

---

## ⚡ Quick Start: 30 Seconds to Run

Get the agent gateway up and running immediately:

```bash
# 1. Clone & enter directory
git clone https://github.com/TheNovaNodes/antigravity-telegram-agent.git
cd antigravity-telegram-agent

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
| [`webhook_guard.go`](webhook_guard.go) | Two-tier webhook immunity suite: Layer 1 startup `deleteWebhook` purge & Layer 2 runtime HTTP 409 Conflict auto-recovery with `AllowedUpdates` enforcement. |

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
| `/refresh_models`| None | Dynamically fetches the latest model list from `agy models`. |
| `/accounts` | `[status\|switch\|check]` | Multi-account quota pool dashboard, active account switching, and live quota health checks. |
| `/usage` | None | Queries and displays current token quota and tier usage. |
| `/clear` | None | Resets session context, terminates background tasks, and issues a fresh conversation UUID. |
| `/resume` | None | Presents an interactive picker of previous sessions sorted by last modification time. |
| `/export` | None | Compiles and sends full conversation transcript as a clean Markdown document. |
| `/rename` | `<name>` | Renames the current session in brain storage (`.title`). |
| `/workspace` | `<path>` | Switches working directory (sandboxed under `AGENTS_DIR` with symlink traversal checks). |
| `/voice` | `[on\|off]`| Toggles persistent voice responses generated via multiple engines (Edge-TTS, Piper, ElevenLabs, Hybrid). |
| `/tts` | `<text>` | Synthesizes arbitrary text into speech and sends as a voice note. |
| `/tts_engine` | `[hybrid\|edge\|piper\|elevenlabs]` | View or switch the active Text-To-Speech engine. |
| `/stop` (or `/cancel`) | None | Gracefully interrupts active execution turn, terminates subprocess process group (`SIGTERM`/`SIGKILL`), salvages output buffer, and preserves conversation context. |
| `/help` | None | Displays comprehensive command reference. |
| `/goal` | `<prompt>` | Runs exhaustive long-running autonomous task without stopping prematurely. |
| `/schedule` | `<duration>` | Sets background timer or recurring cron schedule for task execution. |
| `/browser` | `<url>` | Launches web browser interaction and research workflow. |
| `/plan` | `<goal>` | Generates structured step-by-step implementation plan. |
| `/learn` | `<notes>` | Persists behavioral guidelines and lessons for future agent turns. |
| `/grill_me` (or `/grill-me`) | None | Triggers interactive interview slash-command in Antigravity CLI (registered as bot command in `main.go`, auto-aliased and normalized in `handlers.go`). |
| `/teamwork_preview` (or `/teamwork-preview`) | None | Triggers multi-agent collaboration preview (registered as bot command in `main.go`, auto-aliased and normalized in `handlers.go`). |

---

## 🌾 Harvester Subsystem (`agy-harvester`)

The repository includes a standalone CLI tool `agy-harvester` to parse, classify, sanitize, and bundle conversational output and workspace artifacts from headless environments.

**Build it manually:**
```bash
make build-harvester
```

**Available Commands:**
*   `scan`: Audits multi-account pools and host brain storage to discover unharvested session artifact inventories.
    ```bash
    ./bin/agy-harvester scan --all
    ./bin/agy-harvester scan --all --json
    ```
*   `extract`: Packages the latest session data, runs the document taxonomy classifier, redacts secrets via the secret shield, and bundles the result into a ZIP archive with a `manifest.json`.
    ```bash
    ./bin/agy-harvester extract --session my_session_id --out ./exports/artifacts.zip
    ```
*   `doctor`: Audits and detects orphaned artifacts older than 48 hours stranded in session storage and not committed to Git.
    ```bash
    ./bin/agy-harvester doctor --max-age 48h
    ```

For detailed operational procedures, refer to [docs/HARVESTER_RUNBOOK.md](docs/HARVESTER_RUNBOOK.md).

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
* **Two-Tier Webhook Guard & 409 Conflict Immunity**: Layer 1 preemptively purges lingering webhooks during daemon startup across all swarm bots before initializing Long Polling workers. Layer 2 dynamically intercepts runtime HTTP `409 Conflict` in the polling loop, auto-purging conflicting webhooks with exponential backoff to eliminate crash loops and multi-bot outages.
* **Multi-Account Quota Normalization & Failover Shield**: Decodes upstream CLI `disabled: true` quota states when weekly limits hit 0%, normalizes fractions to `0.0`, and inherits weekly reset windows, preventing false-healthy account nomination and 429 quota exhaustion loops.
* **Multi-Account Conversations Self-Healing & Context Retention (#302)**: Resolves systemd service home path binding in the multi-account pool and dynamically heals broken or dangling symlinks across account profiles, preventing upstream stager crashes and guaranteeing continuous conversation context persistence.

---

## ⚙️ Environment Variables & Configuration

The engine supports flexible configuration through environment variables:

| Variable | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `BOT_TOKENS` | String | `""` | Comma-separated list of Telegram Bot API tokens. |
| `ALLOWED_ADMIN_IDS` | String | `""` | Comma-separated list of authorized Telegram User IDs (Fail-Fast enforced at startup). |
| `TURN_INACTIVITY_TIMEOUT_MINUTES` | Integer | `15` | Inactivity timeout before watchdog salvages buffer (default 15). |
| `TURN_TIMEOUT_MINUTES` | Integer | `15` | Inactivity timeout before watchdog salvages buffer (default 15). |
| `TURN_HARD_DEADLINE_MINUTES` | Integer | `45` | Absolute hard turn deadline (default 45). |
| `AGENTS_DIR` | String | `~/.agents` | Base directory containing agent workspaces and download scratchpads. |
| `BRAIN_DIR` | String | `~/.gemini/antigravity-cli/brain` | Storage directory for conversation logs, titles, and steps. |
| `PROJECTS_DIR` | String | `~/projects` | Base directory for external repository projects and safe `/workspace` boundary. |
| `DATA_DIR` | String | `data` | Directory where SQLite state databases (`sessions_<bot>.db`) are persisted. |
| `AGY_BINARY` | String | `~/.local/bin/agy` | Absolute path to the Antigravity CLI binary (defaults to `~/.local/bin/agy`). |
| `ELEVENLABS_API_KEY` | String | `""` | Comma or newline separated list of ElevenLabs API keys (supports auto-rotation). |
| `ELEVENLABS_BASE_URL` | String | `https://api.elevenlabs.io/v1/text-to-speech` | Configurable TTS endpoint URL (used for reverse proxies and testing). |
| `ELEVENLABS_MAX_CHARS` | Integer | `2500` | Maximum character length threshold for ElevenLabs voice generation. |
| `ELEVENLABS_TIMEOUT_SECONDS` | Integer | `30` | Timeout in seconds for ElevenLabs HTTP requests. |
| `TTS_ENGINE` | String | `hybrid` | TTS engine selection (`hybrid` [default], `edge`, `piper`, `elevenlabs`). |
| `EDGE_TTS_VOICE` | String | `ru-RU-DmitryNeural` | Voice name for Edge-TTS (default `ru-RU-DmitryNeural`). |
| `PIPER_PATH` | String | `""` | Absolute path to the Piper TTS binary. |
| `PIPER_MODEL` | String | `""` | Absolute path to the Piper ONNX voice model. |
| `METRICS_ADDR` | String | `""` | Prometheus metrics bind address (e.g. `:9090`). |
| `METRICS_PORT` | String | `""` | Prometheus metrics port fallback. |
| `ACCOUNTS_DIR` | String | `~/.gemini/antigravity-cli/accounts` | Multi-account pool base directory (default `~/.gemini/antigravity-cli/accounts` or `/etc/antigravity-bot/accounts`). |
| `ECOSYSTEM_INBOX_DIR` | String | `""` | Cross-agent ecosystem inter-bot inbox directory for safe deliver-to-chat file dispatch. |
| `CONVERSATIONS_DIR` | String | `""` | Custom conversation logs directory. |
| `SYSTEM_HOME` | String | `""` | Fallback host home directory when isolating account environments. |
| `SHARED_CACHE_DIR` | String | `""` | Shared caches deduplicated across rotation accounts. |
| `SHARED_GOPATH_DIR` | String | `""` | Shared caches deduplicated across rotation accounts. |
| `SHARED_NPM_DIR` | String | `""` | Shared caches deduplicated across rotation accounts. |
| `SESSION_MAX_IDLE` | Duration | `4h` | Duration before idle session eviction and state archiving (default `4h`). |
| `ENV_FILE` | String | `/etc/antigravity-bot/env` | Production environment file path for systemd supervisor credentials. |
| `ALLOW_DOTENV` | Integer | `0` | Development flag (`1`) allowing `.env` fallback instead of fail-closed `ENV_FILE` requirement. |

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
