# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Documentation & Codebase Invariants Synchronization
- **Codebase-Wide Documentation Re-alignment**:
  - Synchronized `README.md` test coverage badge to verified benchmark of `81.0%` (2289/2827 statements).
  - Documented all 14 targets in `Makefile` and clarified `/workspace` security sandboxing boundaries.
  - Corrected `ACCOUNTS_DIR` (`/etc/antigravity-bot/accounts`) and `AGY_BINARY` (`PATH` resolution priority) in `README.md`.
  - Added Section 13 to `docs/ARCHITECTURE.md` detailing Context Preservation & Session Desync Detection Architecture (#303), complete with sequence diagram and database quarantine mechanisms.
  - Synchronized SQLite schemas (`session_history` unique constraints and `is_orphaned` column; `users` with `is_first_start`) and Webhook Guard function names (`clearWebhookOnStartup`, `recoverFromWebhookConflict`, `getUpdatesWithRecovery`) in `docs/ARCHITECTURE.md`.
  - Rebranded `.github/SECURITY.md` to `antigravity-telegram-agent` and updated supported versions matrix.
  - Hardened local development setup (`ALLOW_DOTENV=1` / `make run`) and test commands in `CONTRIBUTING.md`.
  - Added `session_context_resets_total` to `docs/HARVESTER_RUNBOOK.md` metrics catalog.

### CI/CD Resilience & Repository Hygiene (Issue #301)
- **CI Tooling Hardening & Git Hygiene (Issue #301)**:
  - Added retry loops for static analysis tools installation (`staticcheck`, `gosec`, `govulncheck`) and `govulncheck` scans in `.github/workflows/ci.yml` to eliminate transient network failures.
  - Added job timeout (`timeout-minutes: 5`) and workflow concurrency cancellation to `.github/workflows/secret-scan.yml`.
  - Audited and pruned all 19 stale remote-tracking refs and merged local branches.
  - Synchronized SQLite schemas and webhook guard function names in `docs/ARCHITECTURE.md`.
  - Synchronized `README.md` test coverage badge to verified 81.0%, corrected path resolution descriptions, and documented all Makefile targets.

### Context Preservation & Session Desync Detection (Issue #303)
- **Silent Fallback & Desync Prevention for `--conversation` (Issue #303)**:
  - Detected and alerted on runtime conversation desync in `readStdoutLoop` when `agy` CLI silently rejects `--conversation` due to missing disk state and generates a fresh UUID.
  - Added `session_context_resets_total` Prometheus counter with `bot` label and integrated with `RegisterEngineMetrics`.
  - Added dynamic schema migration for `is_orphaned` column in `session_history` table and automated `markSessionOrphaned` flagging.
  - Filtered orphaned conversations out of `/resume` inline session history list (`handleResumeCommand`).
  - Delivered real-time transparent user notification in Telegram (prepending to stream buffer during turns or sending immediate HTML notice) explaining previous context loss and identifying fresh conversation ID.
  - Guarded callback resumption in `handleCallbackQuery` to warn users when attempting to resume a session whose context is missing on disk.
  - Added comprehensive unit test suite `session_context_desync_test.go` covering clean start non-regression, stream buffering, orphaned exclusion, and alert notifications.

### Bug Fixes & Multi-Account Architecture (Issue #302)
- **Dangling Conversations Symlink Self-Healing & System Home Resolution (Issue #302, PR #303)**:
  - Fixed `getSystemBaseHome()` and `getSharedConversationsDir()` in `account_pool.go` to prioritize `SYSTEM_HOME` and exclude systemd service account directories (`/accounts/`), eliminating path pollution and binding to ephemeral daemon homes.
  - Hardened `ensureSymlink()` to guarantee creation of target directories (`os.MkdirAll`) prior to symlinking, and automatically detect and prune broken/dangling symlinks via `os.Stat()`.
  - Upgraded `EnsureSharedAccountDirectories()` to auto-heal existing broken conversation symlinks across all account profiles, resolving Linux VFS `EEXIST` (`file exists`) errors in upstream `agy` stager (`stager.go:117`) and preventing multi-turn conversation context loss.
  - Added unit test suite (`TestEnsureSharedAccountDirectories_DanglingSymlinkRecovery`, `TestEnsureSharedAccountDirectories_SystemHomeFallback`, `TestEnsureSymlink_DanglingSymlinkSelfHealing`) verifying automatic recovery and path resolution.

### Reliability & Autonomous Resource Management (Issue #304)
- **Memory-Aware Session GC & OOM Prevention (Issue #304)**:
  - Implemented dynamic memory pressure detection via `/proc/meminfo` (`getSystemMemoryStats`).
  - Added aggressive session idle eviction (20m threshold) when system memory pressure exceeds 75% RAM usage or available RAM drops below 1.5GB.
  - Reduced default `SESSION_MAX_IDLE` from 4h to 2h, and accelerated session GC interval to 1m.
  - Guaranteed absolute in-flight turn immunity (`ActiveMessageID != 0` or `!ActiveTurnStart.IsZero()`), protecting active streaming turns from premature eviction.
  - Validated zero context loss on idle eviction due to disk-backed conversation persistence and seamless CLI resurrection.

### Reliability & Multi-Account Quota Management (Issue #298)
- **Disabled Quota Flag Parsing & 429 Failover Recovery (Issue #298)**:
  - Added `Disabled` boolean deserialization in `ModelQuota` and `ParseUsageJSON` to capture `disabled: true` when Antigravity CLI exhausts weekly limits.
  - Normalized `RemainingFraction` to `0.0` when a quota bucket is marked disabled or weekly quota hits 0%, inheriting `reset_time` from the weekly limit window.
  - Excluded accounts with disabled or exhausted Gemini quota from `AcquireAccount` candidate pool and sticky session retention, avoiding erroneous selections.
  - Updated Telegram `/accounts` dashboard formatting to explicitly display `disabled` (e.g., `Gemini: 5h disabled • 7d 0%`) instead of misleading `100%`.
  - Updated auto-failover and cooldown calculation in `session.go` and `account_pool.go` to eliminate the 30-second backoff loop on disabled models.

## [1.3.0] - 2026-09-20

### Security & Reliability (Incident #294 Resolution)
- **Defensive deleteWebhook Guard & 409 Conflict Auto-Recovery (Issues #294, #295, PR #296)**:
  - Implemented Layer 1 startup `deleteWebhook` guard (`webhook_guard.go`) purging stale or unauthorized external webhooks across all swarm bots before launching Long Polling workers.
  - Implemented Layer 2 runtime auto-recovery in polling loop intercepting HTTP 409 Conflict with automatic webhook teardown and retry backoff, preventing infinite loop crashes and bot outages.
  - Enforced explicit `AllowedUpdates` registration (`message`, `edited_message`, `channel_post`, `edited_channel_post`, `callback_query`), ensuring zero dropped inline button callbacks and securing interactive UI menus.
  - Added full unit test suite with mocked Telegram Bot API server (`webhook_guard_test.go`) covering all recovery paths and edge cases.
- **Automated Gitleaks CI Pipeline & Secret Hardening (Issue #273)**:
  - Integrated automated Gitleaks workflow (`.github/workflows/secret-scan.yml`) across all pushes and pull requests.
  - Hardened `.gitignore` and `.gitleaks.toml` rules covering `.secrets*`, `*.token`, `tokens/`, `credentials.json`, and database state files.
- **Child Process Isolation & Secret Leakage Elimination (Issues #281, #282, #283)**:
  - Replaced `os.Environ()` denylist inheritance with strict `buildChildEnv` allowlist (`LANG`, `LC_ALL`, `TZ`, `TERM`, `PATH`, `SYSTEM_HOME`, `USER`, `TMPDIR`, proxies), preventing supervisor credentials (`BOT_TOKENS`, `ALLOWED_ADMIN_IDS`, `ELEVENLABS_API_KEY`) from bleeding into child processes.
  - Suppressed phantom error dispatches in `readStdoutLoop` on idle session teardown (`stream input cancelled: context canceled`) during supervisor restarts.

### Reliability & Lifecycle
- **Graceful SIGTERM Teardown & Maintenance Notice (Issues #284, #290)**:
  - Delivered dedicated user-facing maintenance disclaimer upon daemon shutdown/restart.
  - Suppressed mid-turn SIGTERM agent error dispatches so routine server maintenance does not generate false alarms.
- **Projects Directory Resolution & Workspace Sandboxing (Issues #288, #289)**:
  - Resolved `PROJECTS_DIR` from `SYSTEM_HOME` to support multi-account isolation and arbitrary system users.
  - Enhanced workspace-scoped artifact sandboxing and symlink traversal checks.

### UI/UX & Localization
- **Strict English UI Localization (Issues #291, #292)**:
  - Standardized compaction advice prompts, stream disruption warnings, and artifact captions to English across all bot responses.

### Testing & Infrastructure
- **Go 1.18+ Native Fuzzing Suite (Issue #276)**:
  - Added continuous native fuzz tests for Telegram update dispatchers, TTS cleaners, and Harvester Markdown sanitizers.
- **Zero-Leak Goroutine Verification (Issue #275)**:
  - Integrated `uber-go/goleak` guards across test suites and background cleanup workers.
- **Interactive Account UI & BOLA Authorization Guards (Issue #274)**:
  - Added comprehensive unit tests for account callback routing, Broken Object Level Authorization (BOLA) prevention, and quota mock HTTP recovery.

### Portability & Deployment
- **Portability & Public Release Gate (Issues #277, #278, #280)**:
  - Eliminated hardcoded host paths across path sanitizers.
  - Dynamic `agy` binary discovery resolving through `SYSTEM_HOME` and `PATH`.
  - Rebranded project documentation and repository identifiers to Antigravity Telegram Agent.

### Added
- **Session Artifacts Post-Mortem Exhumation Hook (Issue #269)**:
  - Implemented automatic exhumation hook on `[🆕 New Session]` and `/clear` in accordance with the Session Artifacts Manifesto (`SESSION_ARTIFACTS_MANIFESTO.md`).
  - Added 4-sieve anti-garbage filtering in `pkg/harvester/exhumation.go` (strict `.md`, maturity threshold `>= 200B`, ignore `scratch/`, ignore service masks `*.tmp`, `*.bak`, `*.orig`, `draft_*`, `test_*`, and require `UserFacing == true`).
  - Added No-ZIP Telegram delivery: directly delivers exhumed Markdown documents with emoji classification and clean captions.
  - Implemented artifact alienation (`Move, not Copy`): moves documents to `ecosystem-docs/inbox/<YYYY-MM-DD>_<bot_name>_<slug>.md` and cleans up sidecar `.metadata.json` so no orphaned files remain in session storage (`agy-harvester doctor` clean).
  - Extended Secret Shield to redact Google API keys (`AIza...`) and JWT tokens.
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
  - Added `SECURITY.md` defining vulnerability disclosure standards.
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
