# 🛸 Antigravity Telegram Agent — Architecture Specification

## 1. Executive Summary

The `antigravity-telegram-agent` is an ultra-low-latency, concurrent Telegram Gateway and session supervisor written in pure Go for Google Antigravity CLI (`agy`) headless agentic workflows. 

Rather than embedding heavy runtime dependencies directly into bot processes, the engine acts as an asynchronous multiplexing bridge between the Telegram Bot API and isolated agent CLI subprocesses. Each user interacts with an isolated runtime environment with robust state persistence, non-blocking I/O streaming, real-time Markdown-to-HTML conversion, and self-healing error recovery.

```mermaid
flowchart TD
    TG[Telegram Clients] <-->|Long-Polling / Pure Go| Bot[Telegram Bot Poller]
    Bot -->|Recover / Route| Dispatcher[handleUpdate Dispatcher]
    
    subgraph Handlers ["Modular Handler Layer"]
        Dispatcher -->|Slash Commands| CmdHandler[handleCommand]
        Dispatcher -->|Inline Buttons| CbHandler[handleCallbackQuery]
        Dispatcher -->|Media & .md Exports| MediaHandler[downloadTelegramMedia]
        Dispatcher -->|Prompt Stream| MsgHandler[handleMessagePayload]
        CmdHandler -->|/stop & /cancel| StopHandler[handleStopCommand]
        CbHandler -->|cmd:stop| StopHandler
        CbHandler -->|cmd:retry| MsgHandler
    end
    
    subgraph SessionManager ["Session Manager & Resilience Supervisor"]
        MsgHandler -->|Stdin Pipe JSONL| Session[AgySession]
        Session -->|pgid Process Group| Subproc["os/exec (agy cli)"]
        Subproc -->|Stdout JSONL Stream| StdoutLoop[readStdoutLoop]
        StdoutLoop -->|Throttled Batching 1200ms| Throttler[Stream Throttler (1200ms)]
        Throttler -->|HTML Chunks / EditMessage| TG
        Session -->|15m Inactivity / 45m Hard Deadline| Watchdog[Turn Watchdog & Buffer Salvager]
        Watchdog -->|Timeout Stall / SIGKILL -pgid| Session
        Watchdog -->|Salvaged Buffer & Artifacts| TG
        StopHandler -->|Interrupt Turn / SIGKILL -pgid| Session
        StopHandler -->|Salvaged Buffer & Artifacts| TG
        StdoutLoop -->|Google Cloud 429| Parking[429 Quota Safe Parking]
        Parking -->|Auto-Export Markdown File| TG
    end

    subgraph Storage ["SQLite WAL Storage & File Tree"]
        CmdHandler <-->|PRAGMA journal_mode=WAL| DB[(SQLite sessions.db)]
        CbHandler <-->|Session History & Voice State| DB
        Session <-->|Conversation State| Brain["~/.gemini/antigravity-cli/brain"]
    end

    subgraph TTS ["Audio Engine"]
        StdoutLoop -.->|VoiceReply Enabled| AudioPipeline[Multi-Engine Audio Pipeline (Edge-TTS, Piper, ElevenLabs, Hybrid)]
        CmdHandler -.->|/tts Command| AudioPipeline
        AudioPipeline -->|Voice Note OGG/MP3| TG
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
* **Mutex-Decoupled Process Termination (`AgySession.Kill()`)**: Mutex `session.mu` locks state updates exclusively for fast field mutation (`isAlive = false`, zeroing pointers). Blocking I/O (`Stdin.Close()`) and kernel process signals (`syscall.Kill`) execute outside the lock, preventing thread contention and eliminating potential deadlocks during concurrent stream reads.

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

### 2.2 Subprocess Watchdog & State T Deadlock Immunity
When background or verification commands (e.g. MCP stdio servers, smoke tests) attempt to read `stdin` without a connected TTY or outside foreground job control, the Linux kernel delivers `SIGTTIN` (or `SIGTTOU`), suspending the child in `State: T` (Stopped). If wrapped with commands like `timeout` without the `-k` (`--kill-after`) flag, the process fails to process `SIGTERM`, resulting in an unrecoverable agent turn deadlock.

The engine incorporates a dedicated autonomous watchdog ([`subprocess_watchdog.go`](../subprocess_watchdog.go)):
* **Pure Go `/proc` Traversal**: Iterates `/proc` and parses `/proc/[pid]/stat` directly without external `ps` overhead or shell execution.
* **Process Tree Ancestry Resolution**: Traces parent-child hierarchy to identify descendants belonging exclusively to active agent root sessions (`AgySession`).
* **Two-Phase Forced Termination**: When any descendant process remains suspended in `State: T` or `State: t` beyond the grace period (default 3 seconds), the watchdog issues a sequenced `SIGCONT` (awakening the process) followed by `SIGKILL` (unconditional kernel termination).
* **Background Supervisor Worker**: `StartSubprocessWatchdogWorker` runs continuously on a 2-second ticker, fully integrated into the engine lifecycle alongside session garbage collection.

### 2.3 Inactivity Turn Watchdog & Buffer Salvaging Pipeline
During execution of complex tasks, external tool invocations, or network stalls, an agent process might stall without producing stream output. To prevent deadlocks, `session.go` implements an autonomous **Inactivity Turn Watchdog**:

* **15-Minute Inactivity Window (`turnTimeout = 15 * time.Minute`)**: Evaluated on a 5-second polling loop (`watchdogInterval = 5 * time.Second`), configurable via `TURN_INACTIVITY_TIMEOUT_MINUTES` or `TURN_TIMEOUT_MINUTES`.
* **45-Minute Hard Turn Deadline (`hardDeadline = 45 * time.Minute`)**: Acts as a failsafe against infinite loops, configurable via `TURN_HARD_DEADLINE_MINUTES`. Active turns with continuous progress (`s.LastActivity` fresh) run uninhibited up to this safety ceiling.
* **Granular Heartbeat on All JSONL Events**: Field `s.LastActivity` is atomically refreshed whenever any JSONL step arrives (`init`, `user`, `tool_use`, `tool_result`, `model`, `step_finish`). Long-running tools emitting stdout do not trigger premature timeouts.
* **Buffer Salvaging & Artifact Delivery**: If `time.Since(s.LastActivity) > turnTimeout` or `time.Since(s.ActiveTurnStart) > hardDeadline`, the watchdog triggers fail-safe salvage:
  1. `s.Kill()` sends `syscall.Kill(-pgid, SIGTERM)` followed by `SIGKILL` to eradicate the stuck process group.
  2. The accumulated `s.TextBuffer` is retrieved under lock and appended with a diagnostic reason notice (clearly distinguishing between inactivity and hard turn deadline).
  3. The salvaged content is converted through `MarkdownToTelegramHTML` and flushed to Telegram.
  4. Any created artifacts (`file://...`) in the salvaged text are extracted and delivered via `sendArtifacts`.
  5. The conversation UUID in SQLite is preserved, and the bot immediately returns to ready state.

### 2.4 Stream Auto-Recovery & Quota Safe Parking (429)
The engine provides automated fault recovery across unreliable upstream networks and quota boundaries:

* **Automatic Stream Recovery with Buffer Salvaging (Up to 2 Retries)**:
  When upstream Google Cloud streaming connections sever mid-turn, `readStdoutLoop` detects the drop. If `s.StreamRetries < 2`, the engine increments the counter, preserves the accumulated `s.TextBuffer`, displays a non-destructive auto-recovering status notice without erasing previously generated output, restarts the subprocess (`s.start()`) with the conversation context, and feeds an internal continuation prompt (`The streaming connection was interrupted mid-turn...`). Any new token deltas append seamlessly to the preserved buffer. If retries are exhausted or restart fails, the full accumulated text buffer is salvaged and delivered to Telegram, any referenced artifacts are extracted and sent, and an inline `🔄 Resume task` (`cmd:retry`) button is attached. All CLI print timeouts and generic agent errors likewise salvage accumulated output rather than discarding it.
* **429 Quota Safe Parking**:
  If the model returns a genuine rate limit (`429`, `RESOURCE_EXHAUSTED`, `quota`), the engine automatically triggers `handleExportCommand`, compiles the conversation transcript into a Markdown export document (`session_<title>.md`), and delivers it to the Telegram chat. The active turn is cleanly concluded without corrupting SQLite or leaving hanging processes.
* **Drop-to-Resume Workflow**:
  When a user forwards or uploads any `session_*.md` export file to the chat, `downloadTelegramMedia` detects the session export prefix and injects an automated prompt (`📋 Previous session context loaded from export file...`). The agent ingests the previous transcript and resumes execution seamlessly.

### 2.5 Dual-Circuit Streaming Resilience: In-Stream Fusion & Transcript Fallback (#355)
When the model outputs text containing thinking tag tokens (e.g. `<thought>` or `<think>` inside markdown code or explanations), the upstream `agy` CLI bridge in `stream-json` mode may erroneously switch streaming deltas from `su["text_delta"]` to `su["thinking_delta"]`. To guarantee uninterrupted streaming in real time and 100% response recovery upon turn finalization, the engine implements a dual-circuit resilience architecture:

1. **Circuit 1: In-Stream Fusion (`session.go`)**:
   - In `readStdoutLoop`, when `step_update` contains `su["thinking_delta"]`, the engine checks whether visible response text generation has already begun (`s.TextBuffer != ""`).
   - If text has already started, incoming `thinking_delta` chunks are fused directly into `s.TextBuffer` as visible text continuation while respecting the 1MB buffer ceiling (`maxTextBufferBytes`), updating `s.LastActivity` and notifying `s.UpdateChan` to ensure real-time streaming edits in Telegram never freeze.
   - If `s.TextBuffer` is empty (the agent is in initial pre-response Chain-of-Thought reasoning), `thinking_delta` chunks remain suppressed, preventing internal reasoning leakage into user chats.

2. **Circuit 2: Transcript Fallback (`finalizeTurn` & `pkg/harvester`)**:
   - Upon turn finalization (`event: "result"`), the engine executes a non-blocking disk audit outside `s.mu` lock.
   - It resolves the session transcript path via a multi-account awareness cascade: `s.AccountHomeDir` $\to$ `harvester.DiscoverSession(s.Conversation)` $\to$ `getBrainDir()`, inspecting `transcript_full.jsonl` (and falling back to `transcript.jsonl`).
   - The parser (`harvester.ExtractLastModelResponse`) parses JSONL steps, skipping tool calls with empty content and generic tool output steps, extracting the last model response step (`source == "MODEL"` and `type == "PLANNER_RESPONSE"`).
   - If `len(transcriptText) > len(bufferedText)` and prefix match succeeds, the full 100% response from disk is restored.
   - Restoring full length ensures that long responses ($> 3000$ runes) are accurately routed to Tier 2 (Rich Article) rather than truncated to Tier 1 classic bubbles.

---

## 3. Modular Handler Decomposition

The monolithic message processing loop has been refactored into modular, testable components:

| Handler Function | Responsibility | Concurrency & Security Rules |
| :--- | :--- | :--- |
| `handleStartCommand` | Live agent terminal dashboard (CWD, model, session uptime, steps count, quick action buttons). | Non-blocking file reads from `.title` and `transcript.jsonl`. |
| `handleResumeCommand` | Interactive session picker with relative modification timestamps and `<USER_REQUEST>` prompt titles. | Queries SQLite history + reads `brain` directory. |
| `handleModelCommand` | Dynamic model selection keyboard with modern 3.8/3.1 fallbacks. | Read-locked cache (`modelsMu.RLock()`). |
| `handleRefreshModelsCommand` | Live fetch of supported LLMs from `agy models` using authorized account environment and output sanitization. | Write-locked cache update (`modelsMu.Lock()`), ANSI/spinner cleanup, 3-tier `getPrimaryAccountHome()` resolution. |
| `handleUsageCommand` | Token quota and API tier usage display. | Executes `agy --print /usage`. |
| `handleAccountsCommand` | Multi-account pool manager, live account switching, and quota diagnostics (`/accounts`). | Thread-safe pool reads, interactive action callbacks, and admin-guarded mutations. |
| `handleHelpCommand` | Quick command reference and operational guide. | Pure static format. |
| `handleTTSCommand` | Text-to-Speech synthesis for arbitrary user text (`/tts <text>`). | Hybrid engine (Edge-TTS primary -> Piper TTS CPU failover -> ElevenLabs key pool). |
| `handleTTSEngineCommand` | Inspect or switch active Text-To-Speech engine (`/tts_engine [engine]`). | Thread-safe dynamic engine override and configuration inspector. |
| `handleVoiceToggleCommand` | Persistent toggle for agent voice responses (`/voice [on\|off]`). | Atomic SQLite update to `users.voice_reply` and active in-memory session sync. |
| `handleWorkspaceCommand` | Dynamic agent working directory switching. | Path traversal validation (`isPathUnderRoot`, restricted to `PROJECTS_DIR` or bot's own office). |
| `handleRenameCommand` | Live rename of conversation title in `brain` storage. | Sanitizes title and validates conversation ID against path traversal. |
| `handleExportCommand` | Compiles full conversation transcript JSONL into a clean Markdown file attachment. | Validates session ID format (`isValidSessionID`), writes export file to scratch space. |
| `handleClearCommand` | Session context reset for clean startup. | Cleans session state in DB and memory, launches fresh process without uninitialized conversation ID flags. |
| `handleStopCommand` | Gracefully interrupts active turn without clearing conversation context. | Mutex-decoupled state extraction, `s.Kill()` process group termination, output buffer salvaging, and artifact delivery. |
| `handleCommand` | Centralized strict command token router (`switch cmd`). | Strips `@botName` and matches exact command tokens, eliminating prefix collisions (`/workspacex`, etc.). Translates Telegram-safe underscore aliases (`/grill_me` -> `/grill-me`, `/teamwork_preview` -> `/teamwork-preview`), which are registered as bot commands in `main.go` and normalized in `handlers.go`. Routes `/stop` and `/cancel`. |
| `handleCallbackQuery` | Routes inline button actions (`model:*` [Hot Model Swap], `resume:*`, `ans_id:*`, `cmd:*` including `cmd:stop` [Turn Interruption] and `cmd:retry` [Stream Recovery]). | Broken Object Level Authorization (BOLA) guard (`isSessionOwnedByUser`), safe UTF-8 byte truncation (`truncateUTF8Bytes`), safe prefix slicing, and expired callback query feedback. |
| `extractInboundPayload` | Polymorphic adapter extracting text, command aliases, media attachments, multimodal stickers (.webp/.tgs/.webm), validated geolocation coordinates, and inbound/forwarded `rich_message` content. | Eliminates cyclomatic bloat in `handleUpdate`. Resolves rich messages from raw JSON updates or message attachments, forwards them as first-class prompt text, sanitizes contact rejection (strictly checking `msg.Contact != nil`), and returns informative diagnostics (`⚠️ Unsupported message format`) for unknown formats. Video notes are extracted as `.mp4`, stickers are downloaded (`.webp`, `.tgs`, or `.webm`) into `scratch/downloads/` for multimodal vision as clean attachments (`[Attached File: file://...]`) without semantic hijacking from emoji text strings, and GPS locations are validated (`-90 <= Lat <= 90`, `-180 <= Lon <= 180`) and formatted into structured markers with privacy protection. |
| `isServiceMessage` | Pure Go validator identifying Telegram system and service events (chat created, user joins/leaves, pins, migrations, title/photo updates). | Executed at the start of `handleUpdate`; drops service events silently without sending diagnostic warnings to chat. |
| `isGroupMessageAddressed` | Mention & Reply Guard evaluating whether group messages are targeted to this bot via command, direct reply, or `@botName` mention. | Scoped to groups/supergroups; filters out cross-bot chatter and prevents multi-agent ping-pong loops in Triads. |
| `stripBotMention` | Prompt sanitizer extracting and excising `@botName` mention prefixes and trailing punctuation from user prompts. | Provides clean prompts to the LLM agent while preserving mentions of other bots and email addresses. |
| `downloadTelegramMedia` | Downloads incoming documents, photos, audio, voices, and video notes. Detects session export files (`session_*.md`) and auto-injects context reload prompts for drop-to-resume. | URL scheme & host validation (HTTP/HTTPS only), HTTP status check, 100 MB hard limit, and sandbox download dir. |
| `handleMessagePayload` | Streams user prompt into agent `Stdin` and triggers instant `sendChatAction`. | Enforces JSONL protocol encoding, per-turn voice reply mode without latching, and clean prompt retry on Stdin error. |
| `sendTypingAction` | Background 4-second ticker sending `ChatTyping` / `ChatRecordVoice` while agent thinks. | Non-blocking mutex check. |
| `ExtractAllowedArtifacts` | Validates and dispatches generated documents/artifacts to Telegram. | Fail-closed LFI sandbox (`isPathUnderRoot`) covering `AGENTS_DIR`, `BRAIN_DIR`, and `PROJECTS_DIR` (`!info.IsDir()`). |
| `sendArtifacts` | Sends verified artifacts as Telegram documents. | Opens file descriptors directly (`os.Open`) and streams via `tgbotapi.FileReader`, eliminating path TOCTOU symlink races. |
| `AgySession.start` | Spawns `agy` sub-process with `Setpgid`, `WaitDelay`, and streaming throttler. | Monitors `cmd.Wait()` to clean up zombie `*⏳ Thinking...*` UI states on unexpected process exit. |
| `ReapStoppedSubprocesses` | Inspects `/proc` for stuck descendant processes of active `AgySession` roots in `State: T` / `t`. | Two-phase forced termination (`SIGCONT` + `SIGKILL`) after grace period expiry (default 3s). |
| `StartSubprocessWatchdogWorker` | Background supervisor loop executing `ReapStoppedSubprocesses` on a periodic ticker (2s). | Clean worker shutdown via `stopHousekeeping` channel. |
| `clearWebhookOnStartup` | Preemptive Layer 1 deleteWebhook invocation across all configured bots before Long Polling initialization. | Non-blocking, error-logged, fail-safe startup hook. |
| `recoverFromWebhookConflict` | Layer 2/3 runtime auto-recovery intercepting HTTP 409 Conflict with deleteWebhook self-healing and Layer 4 admin alerts. | Wrapped in `getUpdatesWithRecovery` with retry backoff and admin cooldown alerts. |

---

## 4. SQLite WAL Mode & Persistence Architecture

The engine uses embedded SQLite with Write-Ahead Logging for high-concurrency resilience:

### Database Optimizations:
```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
```
* **WAL Mode (`PRAGMA journal_mode=WAL;`)**: Enables concurrent readers while a writer executes, completely eliminating `database is locked` runtime collisions. Fail-Fast enforced at startup.
* **Busy Timeout (`PRAGMA busy_timeout=5000;`)**: Automatically retries locked transactions for up to 5 seconds before returning an error. Fail-Fast enforced at startup.
* **Synchronous Normal (`PRAGMA synchronous=NORMAL;`)**: Maximizes SQLite write throughput while guaranteeing ACID consistency in WAL mode. Fail-Fast enforced at startup.

### Schema:
```sql
CREATE TABLE IF NOT EXISTS users (
    user_id INTEGER PRIMARY KEY,
    workspace TEXT DEFAULT '',
    model TEXT DEFAULT 'gemini-3.8-flash-high',
    is_first_start BOOLEAN DEFAULT 1,
    session_id TEXT DEFAULT NULL,
    voice_reply BOOLEAN DEFAULT 0
);

CREATE TABLE IF NOT EXISTS session_history (
    user_id INTEGER,
    session_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    is_orphaned BOOLEAN DEFAULT 0,
    UNIQUE(user_id, session_id)
);
```

---

## 5. Hot Model Swap Architecture (Zero-Context-Loss Model Switching)

The engine supports seamless switching of LLM models mid-conversation without loss of history or context:

```mermaid
sequenceDiagram
    autonumber
    actor User as Telegram User
    participant Router as handleCallbackQuery (model:*)
    participant SQLite as SQLite WAL Database
    participant Registry as globalSessions (sessionMu)
    participant Subproc as OS Subprocess (agy)
    participant Brain as brain/<UUID>/ (Storage)

    User->>Router: Tap Model Button (e.g. Claude 3.7 Sonnet)
    Router->>SQLite: updateUserModel(userID, "claude-3-7-sonnet")
    Note over Router,Registry: Retain active user.SessionID (NO random UUID generation)
    Router->>Registry: replaceSession(..., user.SessionID, newModel, ...)
    Registry->>Subproc: Kill old process (syscall.Kill(-pgid, SIGTERM))
    Registry->>Subproc: Spawn new agy (--conversation <SessionID> --model <newModel>)
    Subproc->>Brain: Read previous turns from transcript.jsonl
    Subproc-->>Registry: Init event with preserved conversation_id
    Router-->>User: "🧠 Model switched! ✨ Context preserved!"
    User->>Subproc: Send Turn N prompt
    Subproc-->>User: Stream response with full awareness of Turns 1..N-1
```

### Hot Swap Guarantees:
1. **Zero Context Loss**: Previous messages, tool executions, and user inputs stored in `~/.gemini/antigravity-cli/brain/<UUID>/` are reloaded by `agy` on startup under the new model.
2. **Atomic Process Handoff**: `replaceSession` closes active I/O pipes and terminates the old process group before provisioning the new model runner, avoiding CPU/memory leaks.
3. **Database Consistency**: Only `users.model` is updated in SQLite; `users.session_id` remains immutable across swaps until the user explicitly runs `/clear`.

### Dynamic Model Discovery & Sanitization (`models.go`):
1. **Authorized Environment Resolution (`getPrimaryAccountHome`)**: `agy models` requires an authorized user home directory. The resolver follows a 3-tier strategy:
   - Tier 1: First registered account from `GlobalAccountPool`.
   - Tier 2: First valid profile in `getAccountsDir()` (`/etc/antigravity-bot/accounts`), preferring directories with `.gemini`.
   - Tier 3: Fallback to `getSystemBaseHome()`.
2. **Terminal & Spinner Sanitization (`parseModelsOutput`)**:
   - Strips ANSI escape sequences (e.g. `\x1b[2K`, `\x1b[K`).
   - Normalizes carriage returns (`\r` -> `\n`) to cleanly separate progress overwrite frames.
   - Ignores CLI progress messages (`Fetching available models...`) and invalid/error lines.
   - Validates model identifiers (`isValidModelID`) and deduplicates entries.
3. **Resilient Fallback**: In the event of network isolation or missing CLI binaries, `handleModelCommand` defaults to a modernized fallback trio (`gemini-3.8-flash-high`, `gemini-3.8-flash-medium`, `gemini-3.1-pro-high`).

---

## 6. Streaming Engine & HTML Sanitize Pipeline

Agent standard output streams JSONL objects that are parsed and formatted into Telegram HTML:

```mermaid
flowchart LR
    RawOutput[CLI JSONL Output] --> JsonParser[JSON Stream Parser]
    JsonParser --> FormatEngine[MarkdownToTelegramHTML]
    FormatEngine --> Sanitizer[balanceAndSanitizeTelegramHTML]
    Sanitizer --> Chunker[SplitHTMLChunks max 4000 chars]
    Chunker --> Throttler[1200ms Coalescing Throttler]
    Throttler --> TGAPI[Telegram editMessageText]
```

1. **Tag Balancing & Sanitization**:
   - Telegram HTML strictly allows: `<b>`, `<i>`, `<code>`, `<pre>`, `<a href="...">`, `<tg-spoiler>`, `<blockquote>`.
   - `balanceAndSanitizeTelegramHTML` closes any unclosed tags on chunk boundaries to prevent Telegram API `400 Bad Request: can't parse entities` errors.
2. **Chunking Engine (`SplitHTMLChunks`)**:
   - Accurately partitions content at paragraph boundaries `<p>` or `\n\n` without breaking HTML tags across chunks.
3. **Asynchronous Coalescing Throttling (`1200ms`)**:
   - Evaluates accumulated text buffer every 1200ms (`throttleInterval = 1200 * time.Millisecond`), effortlessly eliminating Telegram API `429 Too Many Requests` rate limits.
   - Prevents empty message race conditions via `hasDelta` tracking and `lastSentText` comparison: edits Telegram exclusively when content has advanced, preserving the initial `*⏳ Thinking...*` spinner until actual content arrives.
   - Injects and maintains the interactive `🛑 Stop` button across all streaming edits.
4. **Notice Delimiters & Telegram HTML Rendering**:
   - System notices (watchdog stalls, truncations, user interruptions) are delimited using pure Markdown (`_[...]_`).
   - `MarkdownToTelegramHTML` properly escapes raw angle brackets before converting Markdown delimiters into compliant Telegram `<i>...</i>` tags, preventing literal `&lt;i&gt;` text rendering.
5. **Tri-Modal Delivery Architecture (`sendAdaptiveResponse`) & Rich Message Engine**:
   - **Tier 1 (Classic Bubble)**: For compact conversational messages ($< 1500$ UTF-8 runes without Markdown structure, or short replies like "Принято", "Задача выполнена"), messages route directly to standard Telegram HTML `sendMessage` (or `editMessageText` when editing an active streaming draft). This preserves lightweight native chat bubbles for short dialogue.
   - **Tier 2 (Rich Article)**: Structured responses $\ge 1500$ runes with Markdown structure (`HasMarkdownStructure`), long texts $\ge 3000$ runes, or responses containing Markdown tables (`HasMarkdownTable`), LLM thinking tags (`HasThoughts`), or HTML expansion $> 4000$ runes route to monolithic `sendRichMessage` / `editRichMessage`. Responses are delivered as a monolithic native article, eliminating messy HTML chunk fragmentation. Markdown tables render as adaptive rounded cards, and `<thought>`, `<think>`, or `<thinking>` blocks are packaged into `RichBlockThinking{Collapsed: true}`.
   - **Zero-Allocation Markdown Structure Detector (`HasMarkdownStructure`)**:
     - Fast, zero-allocation scanner operating on the hot delivery path (~50 ns/op, 0 B/op, 0 allocs/op) using standard library line slicing (`strings.IndexByte`, `strings.TrimSpace`, `strings.HasPrefix`).
     - Detects headings (`^#{1,6}\s`), code fences (```` ``` ````, `~~~`), blockquotes (`^>\s`, `^>$`), bullet lists (`- `, `* `, `+ `, `• `), numbered lists (`^[0-9]{1,9}(\.|\))\s`), horizontal rules (`---`, `***`, `___` $\ge 3$ characters), and bold header plates (`^(\*\*|__)[^\n]+(\*\*|__)`).
   - **In-Place Morphing Guardrail**:
     - In `finalizeTurn` and `sendAdaptiveResponse` for Tier 2: when `activeMsgID != 0`, the engine calls `editMessageText` with parameter `rich_message` (`editRichMessage`) directly on `activeMsgID`.
     - Categorical taboo: deleting `activeMsgID` via `deleteMessage` on Tier 2 is prohibited, completely eliminating UI flicker and preserving the user's visual reading context.
     - **Graceful Fallback**: If `editRichMessage` encounters Telegram API errors or unsupporting servers, the engine seamlessly degrades to editing the existing draft using classic HTML (`sendChunk`) without deleting the draft, guaranteeing 0% response loss.
   - **Tier 3 (Markdown Artifact)**: When responses exceed the Telegram Rich Message hard ceiling ($> 32768$ runes), the delivery router dispatches:
     1. An ergonomic summary preview via `sendRichMessage` safely truncated at 2500 runes using `TruncateMarkdownSafely` (which guarantees rune-safe boundaries and automatically closes any unclosed fenced code blocks `\n```\n`), appended with an informative artifact notice.
     2. Atomic deletion of the streaming draft message (`activeMsgID`).
     3. The complete, unclipped Markdown response written to `scratch/downloads/response_<timestamp>.md` with restricted `0600` file permissions.
     4. Delivery of the full file as a Telegram Document named `agent_response.md`.
   - **Early Disarm & Deduplication Guard**: Prior to `sendAdaptiveResponse` execution, `s.ActiveMessageID` is atomically zeroed (`Early Disarm`) under `s.mu.Lock()` to prevent background streaming throttler ticks from editing the obsolete streaming draft. `s.ActiveTurnStart` is kept active throughout adaptive delivery and artifact dispatch to preserve GC immunity (`ActiveTurnProtection`). In `sendChunk`, errors such as `message to edit not found`, `message can't be edited`, and `message is not modified` suppress secondary message creation, preventing ghost message duplicates in Telegram chats.

---

## 7. Environment & Path Resolution Matrix

All hardcoded filesystem paths and credentials are decoupled and configurable via environment variables:

| Environment Variable | Default Path / Value | Purpose |
| :--- | :--- | :--- |
| `BOT_TOKENS` | `""` | Comma-separated list of Telegram Bot API tokens. |
| `ALLOWED_ADMIN_IDS` | `""` | Comma-separated list of authorized Telegram User IDs (Fail-Fast enforced at startup). |
| `AGENTS_DIR` | `~/.agents` | Base directory containing agent workspaces and download scratchpads. |
| `BRAIN_DIR` | `~/.gemini/antigravity-cli/brain` | Storage for agent conversation logs, titles, and step histories. |
| `PROJECTS_DIR` | `~/projects` | Base directory for project codebases and sandbox boundaries (`isPathUnderRoot`). |
| `DATA_DIR` | `/var/lib/antigravity-bot/data` | Directory where SQLite state databases (`sessions_<bot>.db`) are persisted (isolated outside Git tree). |
| `AGY_BINARY` | `~/.local/bin/agy` (or `PATH`) | Path to Antigravity CLI executable (defaults to `~/.local/bin/agy`). |
| `ELEVENLABS_API_KEY` | `""` | Comma/newline separated list of ElevenLabs API keys (supports automatic rotation). |
| `ELEVENLABS_BASE_URL` | `https://api.elevenlabs.io/v1/text-to-speech` | Configurable base URL for testing and reverse proxies. |
| `ENV_FILE` | `/etc/antigravity-bot/env` | Production secrets environment file. |
| `ALLOW_DOTENV` | `""` | Set to `1` in development to allow fallback to working directory `.env`. |

---

## 8. Secrets Management & In-Depth Defense

The engine implements a strict, fail-closed secrets handling architecture:

1. **Decoupled Secret Storage**: In production, secrets MUST NOT reside adjacent to binaries in the project working directory. Credentials default to `/etc/antigravity-bot/env` (configurable via `ENV_FILE`).
2. **Fail-Closed Permissions**: The engine verifies that the environment file has strict `0600` permissions. If permissions cannot be restricted, the engine terminates immediately (`log.Fatalf`).
3. **Explicit Dev Mode Gate**: Fallback to local `.env` files is only permitted when `ALLOW_DOTENV=1` is explicitly set in the execution environment.
4. **Tooling Verification**: Run `make env-check` to assert that no exposed `.env` files exist in the repository tree before staging or deployments. Detailed provisioning recipes are documented in [`docs/SECRETS.md`](SECRETS.md).

---

## 9. Multi-Account Quota Pool Architecture

To eliminate upstream rate limit deadlocks across Google Antigravity tiers, the engine integrates a production-grade multi-account supervisor ([`account_pool.go`](../account_pool.go), [`account_handlers.go`](../account_handlers.go)):

```mermaid
flowchart TD
    Req[Incoming User Prompt] --> Session[AgySession]
    Session --> Acquire[GlobalAccountPool.AcquireAccount]
    Acquire --> Check{Active Account Healthy?}
    Check -->|Quota > 5% & !Disabled & Active| Execute[Launch Subprocess under Account HOME]
    Check -->|Cooldown / Disabled / Quota == 0| Rotate[Auto-Failover to Next Healthy Account]
    Rotate --> SafePark[Safe Parking & Session Context Retention]
    Rotate --> Execute
    Execute --> Probe[Periodic Background Quota Probe]
    Probe --> AutoRecover{Recovered > 5% & !Disabled?}
    AutoRecover -->|Yes| Unban[Clear CooldownUntil & Restore StateActive]
    AutoRecover -->|No / Disabled| Backoff[Sane Dynamic Backoff / Inherited Reset Window]
```

### Key Capabilities:
1. **Dynamic Account Auto-Discovery**: Automatically traverses `/etc/antigravity-bot/accounts/*/` resolving canonical homes and symlinked accounts (`acc-1`, `acc-2`, etc.).
2. **Dynamic Quota Monitoring (`FetchAccountQuotas`)**: Periodically probes `agy --print /usage` per account, tracking token limits, remaining quotas, and tier utilization.
3. **Stream Interruption & 429 Failover**: When mid-turn SSE sockets drop and retries are exhausted or rate limits occur, the pool dynamically migrates the session to the next available healthy account without losing conversational state.
4. **Auto-Recovery & Sane Cooldowns**: Eliminates multi-hour lock traps by validating quota health (`> 5%`) on background probes, auto-clearing `StateCooldown` and restoring `StateActive`.
5. **Shared Build & Module Caches**: Deduplicates Go build cache, Go module cache (`GOPATH/pkg/mod`), npm, and pip caches across account homes via dynamic symlinking to prevent disk exhaustion.
6. **Disabled Quota Normalization & Cross-Window Shield (#298)**: Deserializes the `"disabled": true` attribute emitted by Google Antigravity CLI when weekly limits hit 0%. Normalizes `RemainingFraction` to `0.0`, flags `Disabled = true`, and inherits `reset_time` from the weekly limit window. Excludes disabled accounts from candidate nomination in `AcquireAccount` and prevents rapid 30-second false-healthy backoff loops in auto-failover logic.
7. **Dangling Symlink Self-Healing & Shared Conversations Integrity (#302)**: Eliminates systemd service home path binding in `getSharedConversationsDir` by prioritizing `SYSTEM_HOME` and host roots over transient `/accounts/` homes. In `EnsureSharedAccountDirectories` and `ensureSymlink`, target directories are pre-created (`os.MkdirAll`) and broken/dangling symlinks are automatically detected via `os.Stat()` and healed, preventing POSIX `EEXIST` crashes in upstream `agy` stager (`stager.go:117`) and guaranteeing seamless multi-turn conversation persistence across account rotations.
8. **Account Lifecycle Management (Freeze / Unfreeze / Delete) & Administrative Immunity (#339)**:
   - **Administrative Freeze (`StateFrozen`)**: `FreezeAccount(accountID)` places a profile on administrative hold, instantly detaching active chat assignments (`activeChat`), terminating running child CLI processes, and evicting in-memory sessions via `ResetAccountSessions(accountID)`.
   - **Absolute Administrative Immunity**: Frozen accounts are completely bypassed during rotation in `AcquireAccount`, `SwitchAccount`, and `PinAccount`. They possess absolute immunity against automatic background cooldown recovery (`StartBackgroundReaper`), periodic quota probe auto-clearing (`FetchAccountQuotas`), and manual cooldown clearing (`ClearCooldown`). Once frozen, an account remains strictly suspended until an administrator explicitly executes `UnfreezeAccount`.
   - **Administrative Unfreeze**: `UnfreezeAccount(accountID)` validates frozen state and restores the profile to `StateActive` (or `StateCooldown` if a future cooldown reset timestamp remains).
   - **Safe Account Deletion & Path Traversal Guards (`DeleteAccount`)**: Enforces `acc.State != StateInUse` to prevent tearing down active turns. Validates `acc.HomeDir` using `filepath.Clean` and `filepath.Rel(p.accountsDir, cleanDir)` to ensure deletion cannot escape the designated profiles folder (`..`, `.`, `/`, root or foreign directories are blocked as security violations).
   - **Zero Central Storage Loss**: In purge mode (`purgeStorage = true`), `os.RemoveAll` safely unlinks account subdirectories without following symlinks, guaranteeing that central host storage (`~/.gemini/antigravity-cli/conversations`) and deduplicated shared caches remain completely intact.
   - **Two-Step Confirmation & Telegram UI**: The interactive `/accounts` dashboard exposes `[❄️ Freeze]`, `[🧊 Unfreeze]`, and `[🗑️ Delete]` actions with an inline two-step confirmation dialog supporting storage preservation (`Keep Storage`) vs. full directory eradication (`Purge Storage & Delete`).
9. **Master-Detail Ergonomic Architecture & Zero Footgun UI (#343)**:
   - **Elimination of Button Wall & Truncation**: Replaces the 30-button wide row layout with a two-tiered **Master-Detail** navigation model in Telegram.
   - **Master Dashboard View (`formatAccountsDashboard`)**: Preserves complete textual quota and pool summaries, while transforming inline keyboard navigation into an ergonomic 2-per-row grid (`[ 🟢 ID 🔒 ] [ ⚡ ID • ]`) with dynamic status badges (`🟢 Active`, `⚡ In-Use`, `⏳ Cooldown`, `🧊 Frozen`, `🔴 Expired`) and chat association markers (`🔒 Pinned`, `• Active`). Includes dedicated rows for global chat actions (`[🔓 Unpin (Enable Auto-Pool)]`) and pool administration (`[🔄 Refresh Quotas]`, `[📥 Ingest Current Login]`).
   - **Interactive Account Card Detail View (`formatAccountCard`)**: Tapping any account button in-place edits the message into a full-width profile card (`acc:manage:<id>`) displaying email, profile path, live status, detailed Gemini/Claude quota windows with UTC reset times, active turn counts, total errors, and last used timestamp.
   - **Full-Width Action Palette**: The detail view provides full-width buttons (`Switch`, `Pin / Unpin`, `Freeze / Unfreeze`, `Delete Account from Pool`, and `[🔙 « Back to Account List]`), completely eliminating Telegram text truncation (`...`).
   - **Zero Footgun & Nil-Safety Guarantee**: Destructive actions (`Delete`, `Freeze`) are strictly quarantined inside the detail card away from everyday account switching. Missing or deleted accounts are intercepted with callback notification alerts and automatic dashboard refreshes without nil pointer dereference.
   - **Context-Aware Deletion Navigation**: Inline confirmation dialogs preserve context via `acc:del_cancel:<id>`, seamlessly returning focus to the specific account card upon cancellation.
   - **Telegram API Tolerance**: Softly suppresses harmless `Bad Request: message is not modified` API errors across in-place view transitions.

---

## 10. Child Process Isolation & Teardown Lifecycle Hardening

To satisfy enterprise DevSecOps standards and eliminate phantom error leakage across supervisor life cycles:

```mermaid
flowchart TD
    Supervisor["Supervisor Daemon (/etc/antigravity-bot/env)"] -->|BOT_TOKENS, ADMIN_IDS, Keys| SupervisorMemory[Protected Supervisor Memory]
    Supervisor -->|Trigger Subprocess| EnvBuilder["buildChildEnv(accHome) Allowlist"]
    EnvBuilder -->|Strict Baseline: LANG, LC_ALL, TZ, TERM| ChildEnv[Child Process Environment]
    EnvBuilder -->|Preserve Host PATH, SYSTEM_HOME, USER, TMPDIR| ChildEnv
    EnvBuilder -->|Isolated HOME, GOPATH, GOCACHE, NPM, PIP| ChildEnv
    ChildEnv --> ChildProcess["Child Process (agy / tools)"]
    SupervisorMemory -.->|BLOCKED / NEVER INHERITED| ChildProcess
```

### 1. Strict `safeEnv` Allowlist (`buildChildEnv`):
- **Elimination of Supervisor Secret Inheritance (#282)**: Replaced vulnerable `os.Environ()` denylist copying with a strict construction allowlist. Subprocesses spawned for agent sessions, quota diagnostics, or account management inherit only explicit baseline variables (`LANG=C.UTF-8`, `LC_ALL=C.UTF-8`, `TZ=UTC`, `TERM=xterm-256color`) and verified host variables (`PATH`, `USER`, `LOGNAME`, `SYSTEM_HOME`, `TMPDIR`, `AGY_BINARY`, `SSH_AUTH_SOCK`, network/SSL proxies).
- **Zero Credential Bleed**: Sensitive tokens (`BOT_TOKENS`, `ALL_TOKENS`, `ALLOWED_ADMIN_IDS`, `ELEVENLABS_API_KEY`, database URLs, OAuth credentials) reside exclusively in supervisor memory and are never exposed to child processes or subagents.

### 2. Teardown & Idle Error Suppression (#281):
- **Phantom Error Suppression**: When the supervisor terminates or restarts (e.g. system upgrades or container restarts), closing `stdin` on `--input-format stream-json` causes `agy` to exit with `stream input cancelled: context canceled`.
- **Turn-Aware Lifecycle Guard**: In `readStdoutLoop`, incoming error events are checked against active turn state (`!s.isAlive || s.ActiveTurnStart.IsZero()`). Teardown artifacts from idle or stopping sessions are logged internally and suppressed, eliminating false-positive `❌ Error from agent` messages in Telegram. Active turn errors continue to trigger auto-recovery (`StreamRecovery`) and user notices without interruption.

---

## 11. Two-Tier Webhook Guard & 409 Conflict Immunity

To prevent routing deadlocks, multi-bot crash loops, and split-brain webhook conflicts across Telegram Bot API instances ([`webhook_guard.go`](../webhook_guard.go)):

```mermaid
flowchart TD
    Start[Daemon Boot] --> Layer1["Layer 1: Preemptive Startup deleteWebhook"]
    Layer1 -->|Purge Lingering Webhooks| Polling["Launch Long Polling Workers (getUpdates)"]
    Polling --> UpdateCheck{Incoming Update or Error?}
    UpdateCheck -->|Update Received| Dispatch[Route to Handlers via AllowedUpdates]
    UpdateCheck -->|HTTP 409 Conflict| Layer2["Layer 2: Runtime Auto-Recovery"]
    Layer2 --> Purge["recoverFromWebhookConflict with Backoff & Admin Alerts"]
    Purge --> Polling
```

### 1. Layer 1: Preemptive Startup Webhook Purge:
- **Daemon Initialization Hook**: On daemon boot, before launching Long Polling routines, every configured bot token in `BOT_TOKENS` executes an automated `deleteWebhook` call with `drop_pending_updates: false` ([`clearWebhookOnStartup`](../webhook_guard.go)).
- **Stale Route Eviction**: Clears lingering webhooks left by previous server migrations, third-party integrations, or unclean supervisor restarts, preventing immediate startup deadlocks.

### 2. Layer 2: Runtime 409 Conflict Auto-Recovery:
- **Dynamic Conflict Interception**: If an external process or orchestrator sets a webhook while the daemon is actively polling, Telegram returns `409 Conflict: can't use getUpdates method while webhook is active`.
- **Zero-Downtime Healing**: The polling loop ([`getUpdatesWithRecovery`](../webhook_guard.go)) intercepts HTTP 409 status codes, invokes [`recoverFromWebhookConflict`](../webhook_guard.go) with unconditional `deleteWebhook` execution, applies exponential retry backoff, and dispatches Layer 4 alerts to authorized administrators with cooldown protection.

### 3. AllowedUpdates Enforcement:
- **Comprehensive UI Event Ingestion**: Explicitly registers `message`, `edited_message`, `channel_post`, `edited_channel_post`, and `callback_query`.
- **Responsive Interactive Menus**: Guarantees that interactive inline button clicks (`cmd:stop`, `cmd:retry`, account switching keyboards) are never dropped by the Telegram gateway.

---

## 12. Memory-Aware Autonomous GC & OOM Prevention Architecture

To prevent systemd/kernel OOM-killer service terminations under multi-bot concurrency and long-running interactive sessions ([`session.go`](../session.go), [`main.go`](../main.go), Issue #304):

```mermaid
flowchart TD
    Tick["GC Worker Ticker (every 1m)"] --> ReadMem["Read /proc/meminfo (getSystemMemoryStats)"]
    ReadMem --> CheckPressure{"RAM Used >= 75% OR Avail < 1.5GB?"}
    
    CheckPressure -->|Yes: Memory Pressure| SetAggressive["Effective Idle Threshold = 20 min (defaultAggressiveMaxIdle)"]
    CheckPressure -->|No: Normal Load| SetNormal["Effective Idle Threshold = 2 hours (SESSION_MAX_IDLE)"]
    
    SetAggressive --> ScanSessions["Scan globalSessions (Lock/Unlock)"]
    SetNormal --> ScanSessions
    
    ScanSessions --> CheckSession{"For Each Session"}
    CheckSession --> CheckTurn{"Active Turn in Flight? (ActiveMessageID != 0 || TurnStart)"}
    
    CheckTurn -->|Yes| Protect["IMMUNE: Skip Eviction"]
    CheckTurn -->|No| CheckIdle{"Idle Time > Effective Threshold?"}
    
    CheckIdle -->|Yes| Evict["Evict: Kill Process & Close stdin"]
    CheckIdle -->|No| Retain["Retain Process"]
    
    Evict --> ZeroLoss["Disk Persisted (transcript.jsonl / SQLite). Resurrects on next turn."]
```

### 1. Dynamic System Memory Sensing (`/proc/meminfo`):
- **Real-Time Diagnostics**: `getSystemMemoryStats()` parses `MemTotal`, `MemAvailable`, `MemFree`, `Buffers`, and `Cached` from `/proc/meminfo` on each 1-minute GC cycle.
- **Adaptive Memory Pressure Detection**: When RAM utilization reaches `>= 75%` or available RAM drops below `1.5 GB`, the GC automatically activates aggressive reclamation mode.

### 2. Multi-Tier Idle Eviction Thresholds:
- **Normal Operating Mode**: Idle sessions are kept alive up to `2 hours` (reduced from 4 hours, configurable via `SESSION_MAX_IDLE`), providing instant responsiveness for standard user interactions.
- **Aggressive Pressure Mode**: When memory pressure is detected, the idle eviction threshold drops dynamically to `20 minutes` (`defaultAggressiveMaxIdle`), immediately reclaiming 150–400 MB RSS per idle `agy` subprocess.

### 3. In-Flight Turn Immunity & Zero Context Loss:
- **Active Turn Protection**: Sessions currently streaming answers or executing tool turns (`ActiveMessageID != 0` or active `ActiveTurnStart`) are strictly immune to GC, even under extreme memory pressure (95%+).
- **Disk-Backed State Persistence**: Terminating idle `agy` subprocesses produces zero context loss because all conversation trajectories and tool calls are persisted to disk (`transcript.jsonl` and SQLite). Upon the user's next message, `acquireSession()` seamlessly resurrects the session (`agy --conversation <id>`) with 100% full context.

---

## 13. Context Preservation & Session Desync Detection Architecture

To eliminate silent conversation history amnesia when `agy` rejects `--conversation <id>` due to missing or wiped disk state ([`session.go`](../session.go), [`handlers.go`](../handlers.go), Issue #303):

```mermaid
sequenceDiagram
    autonumber
    actor User as Telegram User
    participant Handlers as handleMessagePayload / handleCallbackQuery
    participant Session as AgySession (Supervisor)
    participant Subproc as agy CLI Subprocess
    participant SQLite as SQLite Database (session_history)
    participant Prom as Prometheus (session_context_resets_total)

    User->>Handlers: Prompt / Resume (Requested ID: UUID-A)
    Handlers->>Session: Start subprocess with --conversation UUID-A
    Session->>Subproc: exec(agy --conversation UUID-A)
    Note over Subproc: UUID-A missing on disk! Generates fresh UUID-B silently
    Subproc-->>Session: Stdout JSONL Event: {"event": "init", "conversation_id": "UUID-B"}
    Session->>Session: Detect Desync: requestedID != "" && newID != requestedID
    Session->>Prom: RecordSessionContextReset(botName)
    Session->>SQLite: markSessionOrphaned(userID, UUID-A) (is_orphaned = 1)
    alt Active Turn in Flight
        Session->>Session: Prepend warning banner to TextBuffer: "⚠️ [Context lost...] Started fresh session (UUID-B)"
        Session-->>User: Streaming deltas with transparent context loss alert
    else Callback / Command
        Session-->>User: Instant HTML alert: "⚠️ [Context Reset] Previous context not found on disk"
    end
    Session->>SQLite: updateUserSession(userID, UUID-B)
```

### 1. Root Cause & Threat Model:
When Google Antigravity CLI encounters an `--conversation <id>` argument for a session directory that does not exist in `~/.gemini/antigravity-cli/brain/` (e.g. wiped during disk cleanups, dangling symlinks, or cross-account state divergence), the CLI silently falls back to generating a brand new conversation UUID. Without detection, the Telegram gateway previously continued streaming without notifying the user, causing silent amnesia and confusion.

### 2. Autonomous Desync Detection in `readStdoutLoop`:
Upon receiving the CLI `init` JSONL event, `readStdoutLoop` compares `requestedID` against `newID`. If `requestedID != ""` and `newID != requestedID`:
1. **Telemetry Alerting**: Increments the Prometheus counter `session_context_resets_total{bot="..."}` via [`RecordSessionContextReset`](../metrics.go).
2. **Database Quarantine**: Executes `markSessionOrphaned(db, userID, requestedID)`, setting `is_orphaned = 1` in `session_history`.
3. **History Filter**: `handleResumeCommand` excludes orphaned conversations from the `/resume` interactive inline keyboard picker.
4. **Transparent User Alert**:
   - If an active turn is currently streaming (`ActiveMessageID != 0`), a non-destructive warning banner (`⚠️ _[Previous conversation context could not be loaded from storage. Started fresh session (%s)]_\n\n`) is prepended to `s.TextBuffer`.
   - If idle or during session resumption, an immediate high-priority HTML notice is dispatched to the Telegram chat.
5. **Session Re-alignment**: The database `users.session_id` is updated with `newID` so subsequent prompts correctly target the newly initialized conversation.

---

## 14. Inbound Rich Message Ingestion & Contact Guard Sanitization (Issue #362)

To eliminate architectural blindness to Telegram `rich_message` format responses sent between ecosystem agents and resolve misleading contact rejection:

```mermaid
flowchart TD
    UpdateRaw[Raw Telegram Update JSON] --> ParseJSON[ParseUpdatesFromJSON / getUpdatesWithRichMessage]
    ParseJSON --> HasRich{Contains rich_message?}
    HasRich -->|Yes: root / forward / reply| ExtractText[ExtractRichMessageText: markdown / text / thinking]
    ExtractText --> Attach[AttachRichMessage: msg.Text & Bounded Cache]
    HasRich -->|No: standard update| DirectMsg[Standard tgbotapi.Message]
    Attach --> Router[handleUpdate Router]
    DirectMsg --> Router
    Router --> ExtractPayload[extractInboundPayload]
    ExtractPayload --> CheckEmpty{Empty Text & Media?}
    CheckEmpty -->|No: Valid Prompt| Session[Active Session Prompt Dispatch]
    CheckEmpty -->|Yes| CheckContact{msg.Contact != nil?}
    CheckContact -->|Yes| ContactWarn["⚠️ Contacts are not supported..."]
    CheckContact -->|No| UnsuppWarn["⚠️ Unsupported message format..."]
```

### 1. Inbound Rich Message Interception (`webhook_guard.go`, `rich_message.go`):
- **Raw JSON Interception**: The `tgbotapi.Message` Go struct from `github.com/go-telegram-bot-api/telegram-bot-api/v5` lacks a `RichMessage` field, dropping `rich_message` payloads during default unmarshaling. In `webhook_guard.go`, `getUpdatesWithRichMessage` intercepts the raw Telegram API response buffer.
- **Polymorphic Extraction (`ExtractRichMessageText`)**: Extracts content from structured JSON objects (`markdown`, `text`, or fallback `thinking.text`) as well as direct JSON string literals.
- **Origin & Forward Traversal (`EnrichMessageFromJSON`)**: Resolves rich content across all Telegram update topologies: direct messages, `forward_origin`, `forward_from`, `forward_from_chat`, and `reply_to_message`.
- **Bounded Registry (`AttachRichMessage` / `GetAttachedRichMessage`)**: Maintains an in-memory thread-safe bounded FIFO cache (ceiling: 2048 pointers) to prevent memory leaks during long-running server uptime while ensuring `msg.Text` is immediately populated.

### 2. Contact Guard Sanitization (`handlers.go`):
- **False-Positive Elimination**: The `⚠️ Contacts are not supported...` warning is strictly restricted to actual shared contact objects (`msg.Contact != nil`).
- **Informative Diagnostics**: All other unrecognized or empty payload variants receive `⚠️ Unsupported message format. Please send text, media, or supported files.`, preventing user confusion when interacting with unsupported or partial media updates.

### 3. Telegram Bot API Blocks AST Reconstitution (`rich_message.go`, Issue #366):
- **Live Wire Protocol Discovery**: Telegram Bot API sends rich messages in incoming updates as a structured AST tree under `rich_message.blocks` rather than flat Markdown text.
- **Recursive AST Engine (`parseASTBlocks`, `parseInlineText`)**: Reconstructs complete Markdown fidelity from native AST nodes:
  - Block level: `heading` (H1–H6 via `size`), `paragraph`, `list` (ordered/unordered with indentations and multiline blocks), `pre` (fenced code with `language`), `quote` (`> ` blockquotes), and `table` (Markdown tables with headers, separators, and rows).
  - Inline formatting: recursively evaluates nested tokens for `bold` (`**`), `italic` (`*`), `code` (`` ` ``), `link` (`[text](url)`), `strike` / `strikethrough` (`~~`), `underline` (`<u>`), and `spoiler` (`||`).
- **Fail-Safe Integrity**: Built purely on `encoding/json` and `strings.Builder` with zero regex, ensuring high performance, zero allocations for empty passes, and graceful skipping of unrecognized block types without panics.

---

## 15. Group Service Event Silencer & Mention & Reply Guard for Multi-Agent Triads (Issue #368)

In collaborative group chat environments hosting multiple autonomous agents (e.g. the Triad multi-agent cell: `@kairos_brobot`, `@MataHari_gobot`, `@trickster_gobot`), standard Telegram group updates present two critical architectural challenges without strict inbound filtering:
1. **Service Notification Spam**: Telegram emits service events (group creation, member joins/departures, pinned messages, chat migrations, title/photo modifications) without payload text or media attachments. Unfiltered routers misinterpret these system events as invalid user messages and broadcast misleading warnings (`⚠️ Unsupported message format`).
2. **Ping-Pong Loops (Chatter Storm)**: When bots have privacy mode disabled or administrative permissions, every message is received by all agents. Without selective addressing guards, agents react to each other's responses in an infinite ping-pong loop, exhausting model quota and cluttering the chat.

```mermaid
flowchart TD
    Update[Telegram Update] --> MsgCheck{msg == nil?}
    MsgCheck -->|Yes| Drop1[Drop silently]
    MsgCheck -->|No| SvcCheck{isServiceMessage?}
    SvcCheck -->|Yes: Created, Member, Pin, Migrate, Title, Photo| DropSvc[Silent Return: Zero Diagnostics]
    SvcCheck -->|No| GroupCheck{IsGroup || IsSuperGroup?}
    
    GroupCheck -->|No: Private Chat| PrivateFlow[Normal Processing: All User Messages]
    
    GroupCheck -->|Yes: Group Chat| AddressedCheck{isGroupMessageAddressed?}
    AddressedCheck -->|Command: /cmd or /cmd@thisBot| Allow[Process Message]
    AddressedCheck -->|Reply: replyTo.From == thisBot| Allow
    AddressedCheck -->|Mention: text/caption has @thisBot| Strip[stripBotMention: Clean @thisBot prefix]
    AddressedCheck -->|No match: other bot / general chat| DropGrp[Silent Return: Zero Noise]
    
    Strip --> Allow
    Allow --> Payload[extractInboundPayload & Stdin Stream]
```

### 1. Service Message Silencer (`isServiceMessage`):
- Pure Go validator inspecting Telegram service fields: `GroupChatCreated`, `SuperGroupChatCreated`, `ChannelChatCreated`, `NewChatMembers`, `LeftChatMember`, `PinnedMessage`, `MigrateToChatID`, `MigrateFromChatID`, `NewChatTitle`, `NewChatPhoto`, `DeleteChatPhoto`, `MessageAutoDeleteTimerChanged`, `ProximityAlertTriggered`, `VoiceChatScheduled`, `VoiceChatStarted`, `VoiceChatEnded`, `VoiceChatParticipantsInvited`, and `SuccessfulPayment`.
- Executed at the very top of `handleUpdate`: service updates return immediately (`return`) with zero DB access and zero Telegram API calls, maintaining complete silence.

### 2. Group Mention & Reply Guard (`isGroupMessageAddressed`):
- Scoped strictly to group and supergroup chats (`msg.Chat.IsGroup() || msg.Chat.IsSuperGroup()`), leaving private direct messages (`msg.Chat.IsPrivate()`) 100% unaffected.
- Allows execution if and only if one of the following criteria is met:
  1. **Slash Command for this Bot (`isCommandForBot`)**: Matches untargeted commands (`/help`) or commands specifically targeted to this bot (`/help@thisBot`). Ignores commands explicitly addressed to another bot (`/help@otherBot`).
  2. **Direct Reply to this Bot (`isReplyToBot`)**: Checks `msg.ReplyToMessage.From.UserName == botName` (case-insensitive).
  3. **Explicit Bot Mention (`isBotMentioned`)**: Checks `msg.Entities`, `msg.CaptionEntities`, or regex matching `@botName` on text and caption with word boundary constraints (`(?i)(^|[^a-zA-Z0-9_])@botName([^a-zA-Z0-9_]|$)`). Prevents false matches on prefix/substring bots (e.g. `@botName_v2`).
- All other messages are dropped silently (`return`), preventing multi-agent chatter storms.

### 3. Prompt Sanitization (`stripBotMention`):
- In group chats, any `@botName` token (including trailing commas, colons, or punctuation: `@botName, привет` -> `привет`) is cleanly excised from `payload.Text` and `payload.Caption`.
- Other bots' mentions (e.g. `@kairos_brobot and @MataHari_gobot`) and email addresses (`user@botName.com`) remain intact.
- If a user sends only `@botName` without any prompt or attachments in a group, the engine drops the message silently instead of triggering an unsupported format error.




