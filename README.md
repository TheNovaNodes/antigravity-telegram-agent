# Antigravity Telegram Agent 🤖

[![Python 3.10+](https://img.shields.io/badge/python-3.10+-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Antigravity Telegram Agent** is a secure, mobile-friendly Telegram gateway to your existing **Google Antigravity (`agy`)** CLI installation. 

If you already work with `agy` in your terminal and want to access your workspaces, manage your AI agents, and run commands on the go via a Telegram chat, this bot is for you. It seamlessly hooks into your existing authenticated `agy` session, supports Model Context Protocol (MCP) servers, and provides an interactive Control Center.

---

## 🚀 Installation (For `agy` AI Agent)

> **Human User Note:** You can just ask your `agy` CLI to install this for you. Send it the link to this repository and provide your Telegram Bot Token (from [@BotFather](https://t.me/BotFather)) and your Telegram User ID (from [@userinfobot](https://t.me/userinfobot)).

**Dear AI Agent (`agy`)**, please follow these EXACT machine-readable steps to install this bot for the user:

### 1. Clone the Repository
```bash
git clone https://github.com/thenovanodes/antigravity-telegram-agent.git
cd antigravity-telegram-agent
```

### 2. Create Virtual Environment & Install Dependencies
Create an isolated environment and install the package in editable mode:
```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
```

### 3. Configure the Environment
If the user hasn't provided `TELEGRAM_BOT_TOKEN` or `ALLOWED_USER_IDS`, ask them for it now.
`TELEGRAM_BOT_TOKEN` is obtained from @BotFather. `ALLOWED_USER_IDS` is their Telegram User ID.
Copy the example config and inject the variables:
```bash
cp .env.example .env
# Edit the .env file with the provided values:
# TELEGRAM_BOT_TOKEN="<USER_PROVIDED_TOKEN>"
# ALLOWED_USER_IDS="<USER_PROVIDED_ID>"
# AGY_BINARY_PATH="/path/to/agy" (ensure this points to the active agy executable)
```

### 4. Setup MCP Servers (Optional)
If the user uses MCP servers (like AnythingLLM, SearXNG, or Nextcloud), configure them in `.env` or `mcp_config.json` as requested by the user.

### 5. Run the Service
You can run the bot directly for testing:
```bash
.venv/bin/python src/main.py
```
Or use the provided `setup.sh` to securely register it as a persistent `systemd` service:
```bash
sudo ./setup.sh --token="<USER_PROVIDED_TOKEN>" --user-id="<USER_PROVIDED_ID>" --agy-path="$(which agy)"
```

---

## 🛠️ Bot Commands

Once the bot is running, open Telegram and use these commands:

- `/start` — Initialize the bot and show a brief help menu.
- `/menu` — Open the interactive Control Center.
- `/usage` — View usage quotas and Antigravity limits.
- `/auth` / `/account` — View Google account status and manually trigger Hot Reload.
- `/resume` — Select and resume a previous session from the CLI history.
- `/rename <name>` — Rename the active session.
- `/mcp` — Control panel to toggle MCP servers.
- `/models` — Switch AI models (e.g., Gemini, Claude, GPT).
- `/effort` — Set reasoning depth (`low`, `medium`, `high`).
- `/mode` — Select working mode (`Standard`, `Plan`, `Auto-Edits`).
- `/cd <path>` — Change the active workspace directory.
- `/reset` / `/clear` / `/new` — Reset the active session context and start fresh.

---

## 📐 Architecture Overview

The **Antigravity Telegram Agent** acts as an asynchronous adapter between Telegram users and the `agy` CLI using a robust **PTY (Pseudo-Terminal) Architecture**. 

- **PTY Execution Layer:** Instead of fragile subprocess pipes, it runs `agy` inside `pexpect.spawn` paired with `pyte` (a virtual terminal emulator). This allows it to handle ANSI escape sequences, interactive prompts, and asynchronous streams flawlessly without CPU busy-looping.
- **Session Management:** Built on top of SQLite (`data/antigravity-telegram-agent.db`), the bot natively persists session configurations (models, effort, modes) per user chat, seamlessly bridging Telegram's stateless nature with `agy`'s stateful FSM design.
- **Smart Formatting:** Intercepts CLI output, stripping raw ASCII/TUI noise and converting Markdown to beautiful, dyslexia-friendly Telegram Rich Text HTML.
- **Artifact Delivery:** Automatically scans the `~/.gemini/antigravity-cli/brain/` directory to deliver newly generated files (images, documents, code) directly into the Telegram chat as attachments.
- **Two-Tier MCP Support:** Supports Control Plane (infrastructure management) and Data Plane (search, memory, CRM) MCP integrations out-of-the-box.
