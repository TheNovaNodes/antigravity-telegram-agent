# AntigravityTelegramAgent 🤖

**AntigravityTelegramAgent** is an industrial-grade asynchronous Telegram bot bridge for **Google Antigravity (`agy`)**. It is built on a virtual terminal (`pyte`) using a PTY-architecture. It features Model Context Protocol (MCP) integration, persistent session state management via SQLite, multi-level response chunking, operation auditing, and an interactive Control Center.

As a core component of the **Antigravity Agent Ecosystem**, this repository bridges Telegram communication with powerful Antigravity AI agents, and seamlessly integrates with high-performance memory and web-search gateways provided by **TheNovaNodes**.

---

## ⚡ Quick Installation (3 Commands)

### Prerequisites

1. Install the [Antigravity CLI](https://developers.google.com/agy) on your server.
2. Authenticate: `agy auth login`
3. Create a Telegram bot via [@BotFather](https://t.me/BotFather) and save the token.
4. Find out your Telegram User ID via [@userinfobot](https://t.me/userinfobot).

### Installation

```bash
git clone https://github.com/thedoctormes-hue/antigravity-telegram-agent.git
cd AntigravityTelegramAgent
sudo ./setup.sh
```

The installer will interactively prompt you for:
- 🔑 **Bot Token** (from @BotFather)
- 👤 **Your Telegram User ID** (from @userinfobot)

After this, the process is fully automated: creating a virtual environment, installing dependencies, generating configuration, and registering/starting the systemd service.

### Automated Installation (for Ansible or scripts)

```bash
sudo ./setup.sh --token="123456:ABC-DEF" --user-id="173681771"
```

### Verification

```bash
systemctl status antigravity-telegram-agent
```

Open Telegram, find your bot, and send **/start**.

---

## ❓ Troubleshooting

### 1. Bot requests authorization or a Google OAuth link
* **Cause:** `agy auth login` was not run as the same user running the bot, or `$HOME` paths do not match.
* **Solution:**
  1. Run `agy auth login` in the terminal as your regular user.
  2. Rerun the installer to update paths in systemd: `sudo ./setup.sh`

### 2. Access error `Permission denied: .env`
* **Cause:** The `.env` file is owned by the `root` user.
* **Solution:** Run `sudo ./setup.sh` (the script will automatically change ownership to your user).

### 3. `NameError` or outdated code
* **Solution:** Update the code and restart the service:
  ```bash
  git pull && sudo systemctl restart antigravity-telegram-agent
  ```

---

## 🚀 Recommended AI Stack and MCP Architecture

To ensure autonomy, accurate contextual search, and vendor independence, we recommend a triad of MCP services:

```
                       ┌─────────────────────────────────────────┐
                       │        Google Antigravity (agy)        │
                       └───────────────────┬─────────────────────┘
                                           │ (Model Context Protocol)
       ┌───────────────────────────────────┼───────────────────────────────────┐
       ▼                                   ▼                                   ▼
┌─────────────────────────┐       ┌─────────────────────────┐         ┌─────────────────┐
│  nova-anythingllm-mcp   │       │    nova-searxng-mcp    │         │    Nextcloud    │
│  (TheNovaNodes RAG)     │       │  (TheNovaNodes Search)  │         │   (Work CRM)    │
└────────────┬────────────┘       └────────────┬────────────┘         └────────┬────────┘
             │                                 │                               │
  Hybrid Search:                  Metasearch (90+ engines),          Files, Contacts,
  FTS5 + BM25 + Vectors           Deep Research & Markdown           Calendar (CalDAV)
```

---

## 🛡️ Two-Tier MCP Separation: Control Plane & Data Plane

To ensure information security and optimize the Context Window (Context Budget Efficiency), we utilize a two-tier classification of MCP tools:

```
                          ┌───────────────────────────┐
                          │    Two-Tier MCP           │
                          │    Architecture           │
                          └─────────────┬─────────────┘
                                        │
           ┌────────────────────────────┴────────────────────────────┐
           ▼                                                         ▼
┌──────────────────────────────┐                          ┌──────────────────────────────┐
│  1. Control Plane MCP        │                          │  2. Data Plane MCP           │
│  (Infrastructure Management)  │                          │  (Operational Execution)     │
├──────────────────────────────┤                          ├──────────────────────────────┤
│ • Workspace Administration   │                          │ • Knowledge Base Search      │
│ • API Key Management         │                          │   (FTS5 + BM25)              │
│ • Search Engine Configuration│                          │ • Aggregated Metasearch      │
│ • Access Right Isolation     │                          │ • CRM File Read/Write        │
└──────────────────────────────┘                          └──────────────────────────────┘
```

1. **Control Plane MCP (Management)**: Contains administrative functions. It is separated to prevent vulnerabilities such as Prompt Injections and to exclude unnecessary administrative schemas from user dialogue.
2. **Data Plane MCP (Execution)**: Provides the agent with lightweight operational tools only (semantic memory search, web search, reading/writing user files).

---

## 🧠 High-Performance MCP Gateways by TheNovaNodes

For handling semantic memory and web search, we highly recommend specialized MCP repositories provided by **[TheNovaNodes](https://github.com/TheNovaNodes)**, which offer unique architectural and engineering advantages:

### 1. 🧠 Semantic Memory: [`TheNovaNodes/nova-anythingllm-mcp`](https://github.com/TheNovaNodes/nova-anythingllm-mcp)
* **Engineering Features**:
  - **Next-Generation Hybrid Search**: Combines **FTS5 + BM25** lexical search with vector similarity using weighted merge evaluation (RRF — Reciprocal Rank Fusion / Weighted Merge, calibrated against NDCG metrics). This solves the problem of losing exact terms, function names, and part numbers, which is typical of purely vector-based solutions.
  - **Context Assembly**: Automatically expands found matches into full paragraph context.
  - **Gatekeeping & Diagnostics**: Built-in health checks (`gateway_health`) and concurrency limiting (Fan-out Throttle) to protect your AnythingLLM instance.
* **Repository**: [`TheNovaNodes/nova-anythingllm-mcp`](https://github.com/TheNovaNodes/nova-anythingllm-mcp) (PyPI Package: `nova-memory-gateway`).

### 🔍 2. Deep Web Search: [`TheNovaNodes/nova-searxng-mcp`](https://github.com/TheNovaNodes/nova-searxng-mcp)
* **Engineering Features**:
  - **Metasearch across 90+ sources**: Aggregates search results without ads and user tracking.
  - **Deep Research Orchestration**: Supports multi-step orchestrated search with source synthesis and automatic cleaning of JS-pages into Markdown format.
  - **Semantic Memory Fusion**: Allows instant integration of search results into your local knowledge base.
* **Repository**: [`TheNovaNodes/nova-searxng-mcp`](https://github.com/TheNovaNodes/nova-searxng-mcp).

### 💼 3. Work OS & CRM: [`cbcoutinho/nextcloud-mcp-server`](https://github.com/cbcoutinho/nextcloud-mcp-server)
* **Engineering Features**:
  - Provides integration with the Nextcloud personal cloud (file system, CalDAV calendar, Deck tasks, and contacts).
* **Repository**: [`cbcoutinho/nextcloud-mcp-server`](https://github.com/cbcoutinho/nextcloud-mcp-server) (or official [`nextcloud/context_agent`](https://github.com/nextcloud/context_agent)).

---

## 🌟 Features of AntigravityTelegramAgent
- **Model Context Protocol (MCP)**: Full compatibility with the `TheNovaNodes` ecosystem (Control Plane / Data Plane).
- **PTY-Architecture (`pexpect` + `pyte`)**: Terminal emulation for direct interaction with `agy` without additional Gemini API keys, featuring asynchronous non-blocking CPU optimization (`asyncio.sleep` in the PTY stream reading polling loop to eliminate busy-loops).
- **Interactive Control Panel (`/menu`, `/mcp`)**: A modular Telegram interface for configuring models, reasoning depths (`effort`), working modes, and MCP server states.
- **Session Persistence (SQLite)**: The `data/antigravity-telegram-agent.db` database saves user configuration and ensures session resumption after restart.
- **Automatic Auth Hot Reload**: Monitors file signatures of `~/.gemini/antigravity-cli/antigravity-oauth-token` and `settings.json`. If you change the account via `agy auth login` on the server, the bot automatically picks up the new credentials without a manual restart.
- **Dyslexia-Friendly & Telegram Rich Text (HTML) Formatting**: Automatic conversion of Markdown to Rich Text HTML (`<b>`, `<i>`, `<code>`, `<pre>`, `<blockquote>`, `<a href="...">`), auto-highlighting of Latin terms and file paths in `<code>`, and merging of ragged terminal lines into smooth natural paragraphs with generous spacing (`\n\n`) and ASCII-art cleaning (`▄▀▀`).
- **System Error Interception**: Automatic interception of `Eligibility Check` and `Quota Exceeded` with clear, step-by-step recommendations.
- **Smart Hybrid Delivery**: An algorithm that preserves Telegram Rich Text HTML markup for small responses (up to 3800 characters), performs safe paragraph Multi-Chunking for medium responses (3800–8000 characters), and automatically generates an attached `agent_response.md` file with a preview for responses exceeding 8000 characters.
- **Large Output Handling**: The PTY buffer is dynamically expanded (up to 6000 lines) to prevent truncation of long logs and model responses.
- **Artifact Delivery**: Automatic interception and delivery of agent-generated artifact files (`.md`, `.json`, `.py`, `.png`, etc.) from the working session directory `brain/<conversation_id>` directly into the Telegram chat as documents.
- **Operation Auditing (`logs/audit.log`)**: JSON format logging to monitor executed commands and models.
- **Automatic Resource Cleanup**: A background process for removing inactive sessions (Idle TTL > 30 min).

---

## 🏗️ Project Structure
```
AntigravityTelegramAgent/
├── .github/
│   └── workflows/
│       └── ci.yml          # GitHub Actions CI workflow
├── src/
│   ├── config.py           # Environment configuration validation
│   ├── mcp_config.py       # MCP Servers manager (TheNovaNodes & Custom Gateways)
│   ├── mcp_manager.py      # MCP status checking and management module
│   ├── cli_runner.py       # AgySession (PTY-processes agy, pyte and Auth Hot Reload)
│   ├── formatters.py       # Dyslexia-Friendly formatting and system error interception
│   ├── session_manager.py  # SessionManager (Session lifecycle management and Idle TTL)
│   ├── db.py               # SQLite Session persistence (data/antigravity-telegram-agent.db)
│   ├── audit.py            # JSON Auditing (logs/audit.log)
│   ├── handlers.py         # Telegram command and callback handlers
│   └── main.py             # Application entry point and service initialization
├── tests/                  # Suite of automated unittest tests
│   ├── test_audit.py
│   ├── test_auth_hot_reload.py
│   ├── test_chunking.py
│   ├── test_cli_runner.py
│   ├── test_config.py
│   ├── test_db_persistence.py
│   ├── test_formatters.py
│   ├── test_handlers.py
│   ├── test_mcp.py
│   └── test_session_manager.py
├── data/                   # SQLite Database (antigravity-telegram-agent.db)
├── logs/                   # Audit logs (audit.log)
├── mcp_config.json         # Local endpoints for MCP servers
├── antigravity-telegram-agent.service        # systemd unit file
├── pyproject.toml          # Project dependencies
├── .env.example            # Configuration template
└── README.md               # Documentation
```

---

## 🛠️ Bot Commands
- `/start` — Initialize bot and show brief help.
- `/menu` — Interactive Control Center.
- `/usage` — View usage quotas and Antigravity limits (`/usage`).
- `/auth` — Authorization, view Google email, and Hot Reload button.
- `/resume` — Select and resume a session from the CLI history (`conversation_summaries.db`).
- `/rename` — Rename the active session (`/rename New Name`).
- `/mcp` — Control panel for MCP servers.
- `/models` — Switch AI models (Gemini, Claude, GPT).
- `/effort` — Set reasoning depth (`low`, `medium`, `high`).
- `/mode` — Select working mode (`Standard`, `Plan`, `Auto-Edits`).
- `/reset` / `/clear` — Reset active session context.
- `/help` — Help manual.

---

## 🔌 MCP Server Configuration

Parameters for MCP servers are configured via `mcp_config.json` or `.env` environment variables:

```env
# TheNovaNodes AnythingLLM Gateway
ANYTHINGLLM_URL="http://127.0.0.1:3002"
ANYTHINGLLM_API_KEY="your_api_key"

# TheNovaNodes SearXNG Gateway
SEARXNG_URL="http://127.0.0.1:8889"

# Nextcloud CRM Gateway
NEXTCLOUD_URL="http://127.0.0.1:8000"
NEXTCLOUD_USER="username"
NEXTCLOUD_PASS="app_password"
```

---

## 🧪 Testing

Run the full suite of automated tests:
```bash
python -m pytest
```

---

## 🚀 Deployment

> It is recommended to use the automatic installer `setup.sh` (see the "Quick Installation" section at the top of this document).

### Service Management

```bash
# Bot status
systemctl status antigravity-telegram-agent

# Real-time logs
journalctl -u antigravity-telegram-agent -f

# Restart
systemctl restart antigravity-telegram-agent

# Stop
systemctl stop antigravity-telegram-agent
```

### Manual Installation (if setup.sh is not applicable)

```bash
# 1. Virtual Environment and Dependencies
python3 -m venv .venv
.venv/bin/pip install aiogram pexpect pyte python-dotenv

# 2. Configuration
cp .env.example .env
nano .env     # Enter TELEGRAM_BOT_TOKEN and ALLOWED_USER_IDS
chmod 600 .env

# 3. Systemd Service (edit paths in antigravity-telegram-agent.service!)
cp antigravity-telegram-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now antigravity-telegram-agent
```
