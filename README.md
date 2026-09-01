# 🛸 Antigravity Go Telegram Bot Agent

![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go)
![SQLite](https://img.shields.io/badge/SQLite-07405E?style=for-the-badge&logo=sqlite&logoColor=white)
![Telegram API](https://img.shields.io/badge/Telegram_API-2CA5E0?style=for-the-badge&logo=telegram&logoColor=white)
![Systemd](https://img.shields.io/badge/Systemd-Reliable-brightgreen?style=for-the-badge)

**The Ultimate, High-Performance Pure Go Core for the Antigravity Telegram Bot Ecosystem.**

---

## 📖 The Genesis

Initially, the ecosystem relied on a Python-based routing architecture. As the agents grew more autonomous, the sheer volume of stdout streaming caused the OS pipe buffers (64KB) to overrun. Combined with synchronous Telegram API `bot.Send` calls, this resulted in fatal HTTP `429 Too Many Requests` deadlocks.

We engineered this **Pure Go Core** from scratch to solve this. It completely decouples the stdout reader loop from the Telegram API network calls, relying entirely on an asynchronous throttler loop to prevent pipe stalls.

The result? A blazingly fast, deadlock-immune, unified engine capable of running an entire collective of autonomous agents on a single lightweight binary.

---

## ⚡ Core Architecture & Features

### 1. 🚀 Unified Go Engine
A single compiled binary (`new_engine`) driven by a single `systemd` service (`bot_new_engine.service`) now manages **all 8 bots** simultaneously (`Tyler`, `Marla`, `kairos`, `toomynamea`, `Dartanyan`, `Caduceus`, `prometheus`, `NovaNodes`). No more zombie processes or resource-heavy Python workers.

### 2. 🛡️ CWD Sandboxing (The "Personal Office")
Every agent is granted its own isolated sandbox on the host OS. 
- The SQLite database assigns `Workspace: /root/.agents/<botName>`.
- The Go `exec.Cmd` sets the OS-level `Cmd.Dir` to this exact path.
When an agent attempts to write a file or execute a shell command, they are strictly confined to their personal office, preventing cross-contamination between bots.

### 3. 📊 Cyber-Dashboard (`/start`)
The bot features a highly interactive Telegram UI. Typing `/start` renders a dynamic dashboard that:
- Identifies the current Agent and Model.
- Displays the active CWD sandbox.
- Parses the agent's internal `transcript.jsonl` to calculate exact **Step Counts** and **Session Uptime** on the fly.
- Offers an intuitive Inline Keyboard for quick actions (`/model`, `/usage`, `/clear`, `/resume`, `/rename`).

### 4. 🧠 Seamless Model Switching
A unified Single Source of Truth for model selection. Whether the user types `/model` or clicks the inline dashboard button, they are presented with a full arsenal of LLMs:
- **Google Gemini:** `3.7 Flash High/Med`, `3.6 Flash High/Low`, `3.1 Pro High/Low`
- **Anthropic Claude:** `Sonnet 4.6`, `Opus 4.6`
- **Open-Source:** `GPT-OSS 120B`

### 5. 🔄 Asynchronous Throttling
The engine reads raw `JSONL` chunks from the underlying Antigravity CLI (`/root/.local/bin/agy`) and buffers them. An asynchronous goroutine polls the buffer every 100ms and gracefully flushes the concatenated chunks to the Telegram API, effortlessly bypassing rate limits while keeping the agent's stdout pipe flowing freely.

---

## 📂 Project Structure

- `agysessionsstarter.go`: The beating heart of the router. Manages Telegram long-polling, SQLite session states, inline callbacks, and async subprocess execution.
- `formatters.go`: Markdown sanitizers and chunk processors to ensure Telegram doesn't reject malformed model outputs.
- `deploy_all.sh`: The master deployment script. Kills legacy daemons, compiles the new engine, injects all 8 bot tokens, and registers the `systemd` unit.
- `sessions_<botName>.db`: Auto-generated SQLite databases maintaining state (Workspace, Model, SessionID) per user, per bot.

---

## 🚀 Deployment (Blood Rules)

We adhere strictly to the **ПРАВИЛА КРОВИ (Blood Rules)**: No direct pushes to `master`. All deployments go through Pull Requests.

To deploy the unified engine:
```bash
# 1. Pull the latest master (after PR merge)
git pull origin master

# 2. Build the binary
go build -o new_engine .

# 3. Execute the deployment script
bash deploy_all.sh
```

---
*Built with adrenaline, terminal wizardry, and strict CI discipline by the Trickster and ZavLab.* 🎭⚡
