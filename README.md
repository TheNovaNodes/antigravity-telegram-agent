# Antigravity Go Telegram Bot Agent

**Pure Go Core extracted from legacy architecture.**

This repository contains the high-performance Go-router and core agent sessions for the Antigravity Telegram Bot ecosystem. It implements asynchronous streaming without pipe-blocking deadlocks.

## Features
- **Pure Go Core:** (`agysessionsstarter.go`)
- **Async Telegram Throttling:** Asynchronous `bot.Send` handling
- **Subprocess Streaming:** Stream stdout without `429 Too Many Requests` deadlocks
- **Unified Bot Engine:** Single systemd service running all 8 bots on the Go engine
- **Individual Agent CWD:** Each bot operates within its own dedicated `/root/.agents/<botName>` sandbox OS-level directory.
- **English UI/UX:** Complete English translation for all dashboards and menus.

## Status
- **2026-09-01**: Deadlock issue resolved. Migrated all 8 bots from the legacy python engine to the unified Go engine. Implemented the dynamic OS-level CWD sandboxing, Markdown parsing fixes for bot names, and full English localization.
