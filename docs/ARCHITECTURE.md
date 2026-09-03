# 🛸 Antigravity Go Telegram Bot Agent — Architecture Specification

## 1. Executive Summary

The `antigravity-go-tg-bot-agent` is an ultra-low-latency, concurrent Telegram Gateway and session supervisor written in pure Go for Google Antigravity CLI (`agy`) headless agentic workflows. 

Rather than embedding heavy runtime dependencies directly into bot processes, the engine acts as an asynchronous multiplexing bridge between the Telegram Bot API and isolated agent CLI subprocesses. Each user interacts with an isolated runtime environment with robust state persistence, non-blocking I/O streaming, real-time Markdown-to-HTML conversion, and self-healing error recovery.

```mermaid
flowchart TD
    TG[Telegram Clients] <-->|Long-Polling / Pure Go| Bot[Telegram Bot Poller]
    Bot -->|Recover / Route| Dispatcher[handleUpdate Dispatcher]
    
    subgraph Handlers ["Modular Handler Layer"]
        Dispatcher -->|Slash Commands| CmdHandler[handleCommand]
        Dispatcher -->|Inline Buttons| CbHandler[handleCallbackQuery]
        Dispatcher -->|Media & Files| MediaHandler[downloadTelegramMedia]
        Dispatcher -->|Prompt Stream| MsgHandler[handleMessagePayload]
    end
    
    subgraph SessionManager ["Session Manager & Process Supervisor"]
        MsgHandler -->|Stdin Pipe JSONL| Session[AgySession]
        Session -->|pgid Process Group| Subproc["os/exec (agy cli)"]
        Subproc -->|Stdout JSONL Stream| StdoutLoop[readStdoutLoop]
        StdoutLoop -->|Throttled Batching 100ms| Throttler[Stream Throttler]
        Throttler -->|HTML Chunks / EditMessage| TG
    end

    subgraph Storage ["SQLite WAL Storage & File Tree"]
        CmdHandler <-->|PRAGMA journal_mode=WAL| DB[(SQLite sessions.db)]
        CbHandler <-->|Session History & Voice State| DB
        Session <-->|Conversation State| Brain["~/.gemini/antigravity-cli/brain"]
    end

    subgraph TTS ["Audio Engine"]
        StdoutLoop -.->|VoiceReply Enabled| ElevenLabs[ElevenLabs TTS Multi-Key]
        CmdHandler -.->|/tts Command| ElevenLabs
        ElevenLabs -->|Voice Note OGG/MP3| TG
    end
```

---

## 2. Core Architectural Pillars

### 2.1 Concurrency & Race-Free Thread Model
The gateway is engineered under strict Go concurrency invariants (`go test -race` validated):

* **Granular Mutex Protection (`session.mu sync.Mutex`)**: Guards session mutable fields (`Stdin`, `isAlive`, `Conversation`, `BotAPI`, `ChatID`, `ActiveMessageID`, `TextBuffer`, `VoiceReply`).
* **Subprocess Startup Serialization (`session.startMu sync.Mutex`)**: Guarantees that concurrent incoming user prompts cannot spawn duplicate child processes or race during `os/exec.Command.Start()`.
* **Safe Pointer Publishing**: Child `session.Stdin` and `session.Cmd` are published exclusively **after** successful `cmd.Start()`, preventing nil-pointer dereferences in concurrent readers.
* **Process Group Isolation (`Setpgid: true`)**: All spawned `agy` processes are assigned a dedicated process group (`syscall.Kill(-pgid, syscall.SIGKILL)`). On `/clear`, `/workspace`, or daemon shutdown, entire child process trees (including compiler sub-processes and Python workers) are reliably terminated without leaving orphaned zombies.

```mermaid
sequenceDiagram
    participant User as Telegram User
    participant Bot as Bot Poller
    participant Session as AgySession (Mutex)
    participant Subproc as agy Child Process (pgid)
    participant Stream as readStdoutLoop

    User->>Bot: Prompt Message
    Bot->>Session: handleMessagePayload()
    Session->>Session: Lock startMu & session.mu
    alt Process Not Running
        Session->>Subproc: cmd.Start() with Setpgid=true
        Session->>Stream: Launch readStdoutLoop(stdoutPipe)
    end
    Session->>Subproc: Write JSONL Event to Stdin
    Session->>Session: Unlock startMu & session.mu
    Subproc-->>Stream: Stdout JSONL chunks
    Stream->>Bot: Throttled sendChunk (HTML formatted)
    Bot-->>User: Live Edit / Streaming response
```

---

## 3. Modular Handler Decomposition

The monolithic message processing loop has been refactored into modular, testable components:

| Handler Function | Responsibility | Concurrency & Security Rules |
| :--- | :--- | :--- |
| `handleStartCommand` | Live agent terminal dashboard (CWD, model, session uptime, steps count, quick action buttons). | Non-blocking file reads from `.title` and `transcript.jsonl`. |
| `handleResumeCommand` | Interactive session picker with relative modification timestamps and `<USER_REQUEST>` prompt titles. | Queries SQLite history + reads `brain` directory. |
| `handleModelCommand` | Dynamic model selection keyboard. | Read-locked cache (`modelsMu.RLock()`). |
| `handleRefreshModelsCommand` | Live fetch of supported LLMs from `agy --print /models`. | Write-locked cache update (`modelsMu.Lock()`). |
| `handleUsageCommand` | Token quota and API tier usage display. | Executes `agy --print /usage`. |
| `handleHelpCommand` | Quick command reference and operational guide. | Pure static format. |
| `handleTTSCommand` | Text-to-Speech synthesis for arbitrary user text. | Multi-key ElevenLabs rotation. |
| `handleVoiceToggleCommand` | Persistent toggle for agent voice responses (`/voice [on\|off]`). | Atomic SQLite update to `users.voice_reply`. |
| `handleWorkspaceCommand` | Dynamic agent working directory switching. | Path traversal validation (`filepath.EvalSymlinks`, restricted to `AGENTS_DIR`). |
| `handleRenameCommand` | Live rename of conversation title in `brain` storage. | Writes to `.title` atomic descriptor. |
| `handleClearCommand` | Session context reset and new conversation UUID generation. | Replaces session and kills prior process tree. |
| `handleCommand` | Centralized command router. | Returns `bool` for clean pipeline flow. |
| `handleCallbackQuery` | Routes inline button actions (`model:*`, `resume:*`, `ans:*`, `cmd:*`). | Dispatches callbacks without `goto`. |
| `downloadTelegramMedia` | Downloads incoming documents, photos, audio, and voices. | Enforces 100 MB hard limit and sandbox download dir. |
| `handleMessagePayload` | Streams user prompt into agent `Stdin`. | Enforces JSONL protocol encoding. |

---

## 4. SQLite WAL Mode & Persistence Architecture

The engine uses embedded SQLite with Write-Ahead Logging for high-concurrency resilience:

### Database Optimizations:
```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
```
* **WAL Mode (`PRAGMA journal_mode=WAL;`)**: Enables concurrent readers while a writer executes, completely eliminating `database is locked` runtime collisions.
* **Busy Timeout (`PRAGMA busy_timeout=5000;`)**: Automatically retries locked transactions for up to 5 seconds before returning an error.
* **Synchronous Normal (`PRAGMA synchronous=NORMAL;`)**: Maximizes SQLite write throughput while guaranteeing ACID consistency in WAL mode.

### Schema:
```sql
CREATE TABLE IF NOT EXISTS users (
    user_id INTEGER PRIMARY KEY,
    workspace TEXT,
    model TEXT,
    session_id TEXT,
    voice_reply BOOLEAN DEFAULT 0
);

CREATE TABLE IF NOT EXISTS session_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    session_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 5. Streaming Engine & HTML Sanitize Pipeline

Agent standard output streams JSONL objects that are parsed and formatted into Telegram HTML:

```mermaid
flowchart LR
    RawOutput[CLI JSONL Output] --> JsonParser[JSON Stream Parser]
    JsonParser --> FormatEngine[MarkdownToTelegramHTML]
    FormatEngine --> Sanitizer[balanceAndSanitizeTelegramHTML]
    Sanitizer --> Chunker[SplitHTMLChunks max 4000 chars]
    Chunker --> Throttler[100ms Throttle Queue]
    Throttler --> TGAPI[Telegram editMessageText]
```

1. **Tag Balancing & Sanitization**:
   - Telegram HTML strictly allows: `<b>`, `<i>`, `<code>`, `<pre>`, `<a href="...">`, `<tg-spoiler>`, `<blockquote>`.
   - `balanceAndSanitizeTelegramHTML` closes any unclosed tags on chunk boundaries to prevent Telegram API `400 Bad Request: can't parse entities` errors.
2. **Chunking Engine (`SplitHTMLChunks`)**:
   - Accurately partitions content at paragraph boundaries `<p>` or `\n\n` without breaking HTML tags across chunks.

---

## 6. Environment & Path Resolution Matrix

All hardcoded filesystem paths are decoupled and configurable via environment variables:

| Environment Variable | Default Path | Purpose |
| :--- | :--- | :--- |
| `AGENTS_DIR` | `/root/.agents` (or `~/.agents`) | Base directory containing agent workspaces and download scratchpads. |
| `BRAIN_DIR` | `/root/.gemini/antigravity-cli/brain` (or `~/.gemini/antigravity-cli/brain`) | Storage for agent conversation logs, titles, and step histories. |
| `AGY_BINARY` | `/root/.gemini/antigravity-cli/bin/agy` (or `~/.gemini/...` or `PATH`) | Path to Antigravity CLI executable. |
| `ADMIN_USER_IDS` | `""` | Comma-separated list of authorized Telegram User IDs. |
| `ELEVENLABS_API_KEY` | `""` | Comma/newline separated list of ElevenLabs API keys (supports automatic rotation). |
| `ELEVENLABS_BASE_URL` | `https://api.elevenlabs.io/v1/text-to-speech` | Configurable base URL for testing and reverse proxies. |
