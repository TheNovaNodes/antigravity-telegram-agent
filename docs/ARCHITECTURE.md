# Architecture Overview

## Core Philosophy
The `antigravity-go-tg-bot-agent` is designed to be a lightweight, high-performance Telegram proxy for headless agentic CLI workflows (`agy`). Instead of integrating complex agentic logic directly into the Telegram bot, this engine acts as a resilient interface layer. It spawns, manages, and multiplexes isolated standard I/O pipes for each Telegram user directly into a dedicated subprocess.

## High-Level Architecture
1. **Telegram Polling (`tgbotapi`)**: We use a long-polling architecture (Pure Go Mode) with `tgbotapi.NewUpdate(0)`.
2. **Session Manager (`globalSessions` & `sessionMu`)**: A centralized map links `(BotName, ChatID, UserID)` to an `AgySession` struct.
3. **Subprocess Instantiation (`AgySession.start()`)**: Creates an `os/exec` command invoking the CLI binary.
4. **Stream Multiplexing (`readStdoutLoop` & `throttler`)**: 
   - `readStdoutLoop` reads JSONL streams from standard output asynchronously.
   - The stream is decoded and batched.
   - The `throttler` mechanism runs every 100ms and flushes batched edits to the Telegram API to respect API rate limits.
5. **SQLite Persistence**: Used solely to maintain session state (Conversation UUID, Target Workspace, Selected Model) so that hot-reloads and bot restarts don't lose the connection between a user and their agent.

## Concurrency Model
The engine heavily relies on Go primitives (`goroutines`, `channels`, and `sync.Mutex`). 
Each active user has at minimum:
- One goroutine for long-polling.
- One goroutine for `readStdoutLoop` (listening to the agent).
- One goroutine for the stream `throttler` (updating the Telegram UI).
- One goroutine waiting for the subprocess to exit (`cmd.Wait()`).

## Resiliency & Auto-Recovery
- **OOM / Crash Recovery**: Managed by standard `systemd` supervisors (`bot_new_engine.service`).
- **Amnesia Bug Prevention**: The bot detects `429/503/Timeout` errors directly from the agent's output and automatically restarts the session, injecting a synthetic recovery prompt without user intervention.
- **Deadlock Protection**: HTTP clients for `tgbotapi` are strictly configured with timeouts to prevent cascading pipe-blocks.
