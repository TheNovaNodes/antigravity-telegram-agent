# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Harvester Subsystem & CLI (`agy-harvester`) (Issues #244, #245, #246, #247, #248, #258, #259, #260)**:
  - **`pkg/harvester`**: Added core streaming transcript parser, document taxonomy classifier, secret shield sanitizer, and ZIP bundler with `manifest.json`.
  - **`cmd/agy-harvester`**: Added standalone CLI for headless environment harvesting with `scan`, `extract`, and `doctor` commands.
  - **Telemetry**: Integrated Prometheus exporter metrics for Harvester batch operations.
  - **Documentation**: Added operational runbook at `docs/HARVESTER_RUNBOOK.md`.
- **Dashboard & Session UX Enhancements**:
  - Added active account indicator (`👤 Account: <id>`) to the `/start` terminal card with sticky lock indicator (`🔒`) when pinned.
  - Replaced ambiguous `[🧼 Clear]` button with `[🆕 New Session]` in the `/start` inline keyboard and updated eviction notice UX.
  - Added quick-access `[👥 Accounts]` button directly into the `/start` keyboard with callback routing (`cmd:accounts`).
  - Added transcript compaction & stream resilience reminder (`getCompactionHint`): automatically appends an actionable recommendation to export and restart the session when `transcript.jsonl >= 500 KB` or upon mid-turn stream disruptions.

### Fixed
- **Stream Recovery Infinite Loop & Process Race Prevention**:
  - Enforced `maxTurnFailovers = 1` and `maxTurnStreamRetries = 2` to prevent cascading infinite account rotations and account pool thrashing during upstream Google Cloud stream interruptions.
  - Retained `StreamRetries` across failover rotations rather than resetting to 0, bounding the total recovery budget per user turn.
  - Introduced `cmdEpoch` generation counter in `AgySession` and `cmdWait`, ensuring that superseded subprocesses cannot corrupt active session buffers or send spurious restart notices to Telegram during recovery handoffs.

### Added
- **FinOps CI/CD Optimization (Issue #251)**:
  - Added workflow `concurrency` cancellation (`cancel-in-progress: true`) to `.github/workflows/ci.yml`.
  - Added `paths-ignore` for documentation and markdown files (`**.md`, `docs/**`, `.gitignore`, `LICENSE`).
  - Added caching for SAST tools (`staticcheck`, `gosec`, `govulncheck`) to avoid recompilation on every run.
- **Documentation & Security Policy**:
  - Added `.github/SECURITY.md` defining vulnerability disclosure standards.
  - Added Multi-Account Pool architecture and `/accounts` command reference to `README.md` and `docs/ARCHITECTURE.md`.
  - Updated statement coverage metrics to reflect real benchmark (75.4%).

---

## [1.2.0] - 2026-09-10

### Added
- **Multi-Account Quota Pool (`account_pool.go`, `account_handlers.go`)**:
  - Dynamic discovery and health probing across `/etc/antigravity-bot/accounts/*/`.
  - Seamless stream interruption auto-failover to next healthy account (#254, #256).
  - Background quota health checks with automatic unbanning of recovered accounts.
  - Deduplicated Go build/mod, npm, and pip caches across account home directories (#250).
- **Session Eviction & Safe Parking**:
  - Cold session eviction on `/clear` and 4-hour idle session GC (#252).
  - Cross-turn Safe Parking protocol saving active state before cooldown.

### Fixed
- **Usage Command Root Leak**: Prevented `/usage` from silently executing under host `/root` when all pool accounts are resting.
- **Subprocess WaitDelay**: Enforced `WaitDelay: 5 * time.Second` and clean background reaper cancellation on shutdown (#242).

---

## [1.1.0] - 2026-09-08

### Added
- **Hybrid TTS Audio Engine (`tts.go`)**:
  - Primary Edge-TTS with Piper CPU fallback and ElevenLabs multi-key rotation.
- **Autonomous Subprocess Watchdog (`subprocess_watchdog.go`)**:
  - Two-phase `SIGCONT` + `SIGKILL` reaper eliminating `SIGTTIN`/`SIGTTOU` State T deadlocks.
- **Extended Inactivity Turn Watchdog**:
  - 15-minute inactivity window and 45-minute hard turn safety ceiling.

---

## [1.0.0] - 2026-09-01

### Added
- Initial release of the Pure Go Antigravity Telegram Bot Agent.
- Asynchronous 1200ms coalescing stream throttler.
- Mutex-decoupled non-blocking I/O and process group (`pgid`) isolation.
- SQLite WAL mode session persistence.
