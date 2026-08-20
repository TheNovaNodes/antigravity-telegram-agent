---
name: manus-outsourcer
description: >-
  Use this skill to delegate work to Manus AI, our humble outsourcer agent.
  Triggered when the user mentions "Manus", "Манус", "outsourcer", or asks to
  delegate wide research, web scraping, or long-running cloud tasks.
---

# Manus Outsourcer (Cloud Swarm)

This skill empowers you to offload heavy, time-consuming tasks (like deep research, competitive analysis, or web scraping) to Manus AI. Manus acts as our "humble outsourcer" — a cloud worker that does the tedious tasks while you remain the Architect.

## 🚀 How to Delegate a Task

We use a round-robin Python script (`dispatcher.py`) that balances load across 5 different Manus API keys stored in our `lab-vault`.

### Execution Pipeline

You **must** use the `with-secret` wrapper to securely inject the keys from the RAM disk.

**Step 1. Dispatch the Task (Synchronous)**
```bash
with-secret Yrvxb0 --env MANUS_KEYS -- python3 .agents/skills/manus-outsourcer/dispatcher.py "Твой промпт для Мануса"
```
*(Note: `Yrvxb0` is the default pointer for the "Manus five api key" secret in the vault).*

**Step 2. Schedule a Poller (Asynchronous)**
The dispatcher will return a `task_id` (e.g., `Z8jcVXob...`). Manus can take anywhere from 5 minutes to 2 hours.
DO NOT wait synchronously. Instead, use your `schedule` tool to set a timer (e.g., 10 minutes) with a prompt like:
"Run manus poller for task_id Z8jcVXob...".

When you wake up, execute the poller:
```bash
with-secret Yrvxb0 --env MANUS_KEYS -- python3 .agents/skills/manus-outsourcer/poller.py "TASK_ID"
```
If the task is still running, reschedule yourself for another 10 minutes. If completed, analyze the JSON result.

## 🛠️ When to use this skill
- The user explicitly says "Нам нужен Манус" or "Делегируй Манусу".
- You need to scrape multiple websites or read external API documentation that isn't easily accessible via standard `curl`.
- You need to run a task that takes hours and you don't want to block your local Antigravity runtime.
