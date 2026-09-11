# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
