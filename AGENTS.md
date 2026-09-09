# TheNovaNodes AGENTS.md Manifest

## Part 1: TheNovaNodes Core Invariants (Universal Standard)
1. **Strict Git Flow (ПРАВИЛА КРОВИ):**
   - NEVER push directly to main or master branches.
   - All changes must go through dedicated branches (`feat/...`, `fix/...`, `docs/...`) and Pull Requests.
   - NEVER merge PRs without explicit approval from ЗавЛаб.
   - No force-push on upstream branches.
2. **Security & Credential Hygiene:**
   - NEVER hardcode or log passwords, tokens, or master keys (especially `TELEGRAM_BOT_TOKEN`, `GITHUB_PAT`, backend API keys).
   - All credentials must be loaded dynamically from environment variables or protected storage.
3. **Deadlock & Timeout Guardrails:**
   - All outbound network calls, subprocesses, and commands must have hard timeouts (e.g. `timeout -k 2s 15s`).
   - Never hold mutexes across blocking I/O, process kills, or network requests.
4. **Verification Without Absurdity:**
   - Always verify changes with native project tools (`go test -v ./...`, `go vet ./...`).

## Part 2: Repository Specific Directives (antigravity-go-tg-bot-agent)
- **Engine Architecture:** Headless Linux multi-bot daemon with account pooling and auto-rotation.
- **Session Lifecycle:** Never wipe `session_id` destructively during account rotation. Clean memory safely without unbinding user session state.
- **Watchdog Guardrails:** 15m inactivity TTL, 45m hard deadline per turn. If a turn stalls, harvest and salvage stdout buffer and artifacts before cleanup.
- **Streaming Throttler:** 1200ms throttled edit message cycle; always assign `ActiveMessageID` even for downloaded media.

## Part 3: Verification & The Golden Loop
Execute the following verification steps in your terminal before committing or submitting a PR:
1. `go vet ./...`
2. `go test -v ./...`
Ensure all checks pass cleanly with zero failures.
