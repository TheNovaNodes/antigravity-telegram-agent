# Antigravity Go Telegram Bot Agent

**Pure Go Core extracted from legacy architecture.**

This repository contains the high-performance Go-router and core agent sessions for the Antigravity Telegram Bot ecosystem. It implements asynchronous streaming without pipe-blocking deadlocks.

## Features
- Pure Go core (`agysessionsstarter.go`)
- Asynchronous Telegram throttling and `bot.Send` handling
- Subprocess stdout streaming without `429 Too Many Requests` deadlocks

## Status
- Deadlock issue with `ActiveMessageID` initialization and synchronous API calls has been fully resolved (2026-09-01).
