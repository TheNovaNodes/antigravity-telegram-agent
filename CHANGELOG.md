# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Group Service Event Silencer & Triad Mention & Reply Guard (Issue #368)
- **Telegram Service Message Silencer (`handlers.go`)**:
  - Implemented `isServiceMessage(msg *tgbotapi.Message) bool` detecting all Telegram system and service events (`GroupChatCreated`, `SuperGroupChatCreated`, `ChannelChatCreated`, `NewChatMembers`, `LeftChatMember`, `PinnedMessage`, `MigrateToChatID`, `MigrateFromChatID`, `NewChatTitle`, `NewChatPhoto`, `DeleteChatPhoto`, `MessageAutoDeleteTimerChanged`, `ProximityAlertTriggered`, `VoiceChatScheduled`, `VoiceChatStarted`, `VoiceChatEnded`, `VoiceChatParticipantsInvited`, `SuccessfulPayment`).
  - Silently dropped service events (`return`) at the entry point of `handleUpdate` before database lookup or payload extraction, completely eliminating bogus `⚠️ Unsupported message format` warning spam when creating group chats or adding bots.
- **Group Mention & Reply Guard for Triads (`handlers.go`)**:
  - In group chats (`msg.Chat.IsGroup() || msg.Chat.IsSuperGroup()`), restricted message processing strictly to messages explicitly addressed to the bot:
    1. Direct commands (`msg.IsCommand()`) that are untargeted or specifically targeted to `@botName` (`isCommandForBot`), silently ignoring commands addressed to other bots (`/command@other_bot`).
    2. Direct replies to messages originating from this bot (`isReplyToBot`).
    3. Explicit mentions (`@botName`) across message entities, caption entities, and text/captions (`isBotMentioned`).
  - Silently dropped unaddressed group messages and cross-agent chatter, preventing infinite ping-pong loops and quota exhaustion in multi-agent group chats.
  - Handled messages without explicit `From` user safely (`msg.SenderChat` fallback and nil pointer guard).
- **Prompt Sanitization (`handlers.go`)**:
  - Implemented `stripBotMention(text, botName string) string` excising `@botName` prefix, middle, or suffix tokens along with punctuation (`@botName, `, `@botName: `) before dispatching to `handleMessagePayload`.
  - Preserved mentions of sister bots (e.g. `@kairos_brobot`) and email addresses (`user@botName.com`).
  - Silently dropped empty prompt submissions containing only the bot mention in groups without triggering unsupported format diagnostics.
- **Zero Regression Invariant**:
  - Guaranteed that private 1-on-1 chats (`msg.Chat.IsPrivate()`) remain 100% unaffected, processing all user prompts, media, and slash commands normally.
- **L1 Unit Test Suite (`handlers_test.go`)**:
  - Added `TestIsServiceMessage` validating service event identification across 18 distinct event variants.
  - Added `TestStripBotMention` verifying prompt prefix stripping, punctuation removal, case-insensitivity, and other-bot preservation.
  - Added `TestGroupMentionAndReplyGuard` verifying zero Telegram API calls on service events, unaddressed chatter, other-bot commands, and other-bot replies, while verifying normal delivery on direct commands, mentions, bot replies, and private chats.

### Telegram Bot API Blocks AST Parser for Inbound Rich Messages (Issue #366)
- **Recursive Blocks AST Parser (`rich_message.go`)**:
  - Implemented `parseASTBlocks` and `parseInlineText` to compile Telegram Bot API's native `rich_message.blocks` AST tree back into clean, canonical Markdown.
  - Node support: `heading` (H1-H6 with `size`), `paragraph` (plain strings and nested inlines), `list` (ordered/unordered with indentations and multiline blocks), `pre`/`code` (fenced code blocks with language), `quote`/`blockquote` (`> ` lines), and `table` (header, rows, and cell structures).
  - Inline tokens: nested recursive support for `bold` (`**`), `italic` (`*`), `code` (`` ` ``), `link` (`[text](url)`), `strike`/`strikethrough` (`~~`), `underline` (`<u>`), and `spoiler` (`||`).
  - Zero Dirty Hacks & Fail-Safe: implemented exclusively using standard library `encoding/json` and `strings.Builder` without fragile regex, safely bypassing unknown blocks without panics.
- **L1 Regression & Integration Tests**:
  - Added `TestExtractRichMessageText_TelegramBotAPI_BlocksAST` verifying the exact JSON AST payload from the `@MataHari_gobot` incident.
  - Added `TestExtractRichMessageText_BlocksAST_AdvancedNodes` and `TestParseMessageFromJSON_BlocksAST` in `rich_message_test.go`.
  - Added `TestHandleUpdate_InboundRichMessage_BlocksAST_MataHariForwarded` in `handlers_test.go` verifying end-to-end prompt delivery into the session without false-positive contact or unsupported format warnings.

### Inbound Rich Message Ingestion & Contact Guard Sanitization (Issue #362)
- **Inbound Rich Message Parser & Ingestion Pipeline (`rich_message.go`, `webhook_guard.go`)**:
  - Intercepted raw update and message JSON in `webhook_guard.go` via `getUpdatesWithRichMessage` and `ParseUpdatesFromJSON`, overcoming `go-telegram-bot-api`'s lack of native `rich_message` field in `tgbotapi.Message`.
  - Implemented `ExtractRichMessageText` supporting structured objects (`markdown`, `text`, `thinking`) and raw JSON string primitives.
  - Implemented `EnrichMessageFromJSON` to extract rich content from direct messages, `forward_origin`, `forward_from`, and `reply_to_message`.
  - Added thread-safe bounded registry (`AttachRichMessage` / `GetAttachedRichMessage`) with FIFO eviction (2048 entries ceiling), guaranteeing zero memory leaks over long daemon runtimes.
- **Inbound Payload Extraction (`handlers.go`)**:
  - Updated `extractInboundPayload` to automatically resolve attached rich message content when `msg.Text` is empty, covering direct messages, forwarded messages from sister agents, and replied quotes.
  - Delivered forwarded rich messages directly into active agent session context as standard text prompts without loss or degradation.
- **Contact Guard Sanitization & Unsupported Format Diagnostics (`handlers.go`)**:
  - Fixed false-positive contact rejection: restricted `⚠️ Contacts are not supported...` warning strictly to actual shared contacts (`msg.Contact != nil`).
  - Added informative warning `⚠️ Unsupported message format. Please send text, media, or supported files.` for unrecognized payloads or empty media, eliminating misleading contact errors.
- **L1 Regression Suite**:
  - Added `TestExtractRichMessageText`, `TestAttachAndGetAttachedRichMessage`, `TestParseMessageFromJSON_RichMessageVariants`, and `TestParseUpdatesFromJSON_WrappedAndRaw` in `rich_message_test.go`.
  - Added `TestExtractInboundPayload_RichMessage_DirectAndForwarded`, `TestHandleUpdate_InboundRichMessage_DirectAndForwarded_SessionDelivery`, and `TestHandleUpdate_ContactVsUnsupported_StrictDoD` in `handlers_test.go`.
  - Updated `TestHandleUpdate_UnsupportedMedia` and `TestHandleUpdate_InvalidLocation` in `handlers_test.go` to assert new sanitized warning semantics.

### Rich Article First Routing, Zero-Allocation Markdown Structure Detector, & In-Place Morphing (Issue #357)
- **Zero-Allocation Markdown Structure Scanner (`rich_message.go`)**:
  - Implemented `HasMarkdownStructure(text string) bool` via standard library zero-allocation line slicing (`strings.IndexByte`, `strings.TrimSpace`, `strings.HasPrefix`, `unicode/utf8`):
    - Headings: `^#{1,6}\s`
    - Code fences: ```` ``` ```` or `~~~`
    - Blockquotes: `^>\s` or `^>$`
    - Bullet lists: `- `, `* `, `+ `, `• `
    - Numbered lists: `^[0-9]{1,9}(\.|\))\s`
    - Horizontal rules: `---`, `***`, `___` ($\ge 3$ characters, optional internal spacing)
    - Bold header plates: `^(\*\*|__)[^\n]+(\*\*|__)`
    - Markdown tables: cached pipe check + `HasMarkdownTable(text)`
  - Benchmark performance: 50.9 ns/op, 0 B/op, 0 allocs/op.
- **Rich Article First Routing Policy (`rich_message.go`)**:
  - Calibrated `RichMessageThreshold` from 3000 down to 1500 runes.
  - Updated `DetermineDeliveryTier` to route structured responses $\ge 1500$ runes to **Tier 2 (Rich Article)** when `HasMarkdownStructure(text)` is true.
  - Preserved **Tier 1 (Classic Bubble)** strictly for short (< 1500 runes) flat conversational replies without Markdown structure, or as graceful fallback.
- **In-Place Morphing Guardrail (`rich_message.go`)**:
  - Added `editRichMessage`: edits existing streaming draft in-place via Telegram Bot API `editMessageText` with `rich_message` payload.
  - Eliminated the destructive `deleteMessage + sendRichMessage` anti-pattern for Tier 2: when `activeMsgID != 0`, `sendAdaptiveResponseWithWorkspace` invokes `editRichMessage` directly on `activeMsgID`, completely removing UI flicker.
  - Graceful Fallback: if `editRichMessage` returns an error, the engine seamlessly degrades to editing the existing draft using classic HTML (`sendChunk`) without deleting the draft.
- **Strict Telegram Mock Server & Regression Suite**:
  - Enhanced `createStrictTelegramMockServer` in `test_helpers_test.go` to parse, validate, and track `editMessageText` with `rich_message`.
  - Added DoD test matrix `TestDetermineDeliveryTier_DoDMatrix` in `rich_message_test.go` covering flat short text, flat 1600 runes, H2/H3 headings 1600 runes, task lists 1800 runes, code blocks, and >32KB payloads.
  - Added comprehensive positive and negative test cases in `TestHasMarkdownStructure` and zero-allocation benchmark `BenchmarkHasMarkdownStructure`.
  - Added In-Place Morphing regression tests in `rich_message_test.go` (`TestSendAdaptiveResponse_ActiveMsgID_InPlaceMorphing`, `TestSendAdaptiveResponse_InPlaceMorphing_Fallback`, `TestDelivery_Tier2_RichArticleAndThoughtCollapsing`) and `session_test.go` (`TestSession_FinalizeTurn_EarlyDisarm_PreventsRaceAndDuplicates`, `TestSession_FinalizeTurn_TranscriptRecovery_DropRestoration`).

### Account Pool Leak Prevention, DB Session Guard, and Telegram Escaping (Issue #359)
- **Account State & Turn Release in `/usage` (`handlers.go`)**:
  - Bound deferred `GlobalAccountPool.ReleaseAccount(acquiredAccID)` immediately after successful account acquisition in `handleUsageCommand`.
  - Guaranteed active accounts return to `StateActive` and decremented `ActiveTurns`, preventing permanent leaks into `StateInUse`.
- **Database Wipe Guardrail in `resetChatSessionCache` (`account_handlers.go`)**:
  - Implemented fail-safe guardrail when `userID == 0`: bypassed unconditional SQLite `UPDATE users SET session_id = NULL`, protecting other users from session destruction.
  - Scoped session cache eviction exclusively to in-memory `globalSessions` for the relevant chat.
- **Telegram Entity Escaping & Markdown Hardening (`handlers.go`, `formatters.go`, `account_handlers.go`)**:
  - Added `escapeMarkdown` helper in `formatters.go` for Telegram legacy Markdown parse mode (`\`, `_`, `*`, `` ` ``, `[`).
  - Escaped `sessionTitle` in `handleExportCommand` before injecting into document captions.
  - Wrapped `acc.ID`, `acc.Email`, `acc.HomeDir`, and command target identifiers with `html.EscapeString` in all `/accounts` master-detail templates.
- **Loopback-Only Prometheus Metrics Binding (`metrics.go`)**:
  - Normalized addresses with port only (`:PORT` or `PORT`) to `127.0.0.1:PORT`, preventing unintentional binding to `0.0.0.0`.
- **Artifact Security Hardening (`session.go`)**:
  - Excluded `.env` from text artifact extension whitelist and added explicit guardrail blocking `.env*` files from automatic Telegram dispatch.
- **L1 Regression Test Suite**:
  - Added `TestHandleUsageCommand_ReleasesAccount` and `TestHandleExportCommand_MarkdownEscapedTitle` in `handlers_test.go`.
  - Added `TestResetChatSessionCache_ZeroUserID_PreservesOtherUsers` and `TestAccountsDashboard_HtmlEscaping_MaliciousEntities` in `account_handlers_test.go`.
  - Added `TestStartMetricsServer_LoopbackNormalization` in `main_test.go`.
  - Added `TestSendArtifacts_BlocksDotEnv` in `session_test.go`.

### Dual-Circuit Streaming Resilience & Transcript Fallback (Issue #355)
- **In-Stream Fusion (`session.go`)**:
  - Enhanced `readStdoutLoop` to handle `step_update` events with `su["thinking_delta"]`: when response text generation has already begun (`s.TextBuffer != ""`), incoming thinking deltas are fused directly into `s.TextBuffer` as visible text continuation.
  - Enforced 1MB buffer ceiling (`maxTextBufferBytes`), refreshed `s.LastActivity`, and signaled `s.UpdateChan` to ensure real-time streaming edits in Telegram never freeze when thinking tags (`<thought>`, `<think>`) are emitted in the response.
  - Suppressed thinking deltas when `s.TextBuffer == ""` to safeguard internal Chain-of-Thought reasoning from leaking into chat.
- **Fail-Safe Transcript Fallback (`finalizeTurn` & `pkg/harvester`)**:
  - Implemented multi-account aware session transcript resolver (`resolveSessionTranscriptPath`): cascade `s.AccountHomeDir` $\to$ `harvester.DiscoverSession(s.Conversation)` $\to$ `getBrainDir()`, preferring untruncated `transcript_full.jsonl` with fallback to `transcript.jsonl`.
  - Added `harvester.ExtractLastModelResponse`: scans JSONL steps with an 8MB buffer, skipping empty tool calls and generic tool outputs, extracting the final model response step (`source == "MODEL"` and `type == "PLANNER_RESPONSE"`).
  - Enhanced `finalizeTurn` to verify and restore the complete 100% response from disk outside `s.mu` lock whenever the transcript has more content than the in-memory buffer (`len(transcriptText) > len(bufferedText)`), restoring Tier 2 Rich Article routing for long messages (> 3000 runes).
  - Fail-safe error handling: invalid paths, corrupted JSONL lines, and EOF errors gracefully fall back to in-memory buffer without panics.
- **Regression Test Suite**:
  - Added unit tests in `pkg/harvester/transcript_test.go` (`TestExtractLastModelResponse`).
  - Added unit tests in `session_test.go`: `TestSession_Stream_InStreamFusion_ThinkingDelta`, `TestSession_FinalizeTurn_TranscriptFallback_Recovery`, and `TestSession_FinalizeTurn_TranscriptFallback_FailSafe`.

### Robust Markdown Telegram HTML Parser & CommonMark Compliance (Issue #353)
- **Deterministic Two-Phase Parser (`BlockLexer` & `InlineScanner`)**:
  - Implemented standalone `parser.go` with deterministic CommonMark 0.31.2 compliant block lexing and inline scanning, completely eliminating fragile regular expressions for code fences and inline code spans.
  - **CommonMark 4.8 Code Spans & Paragraph Reset**:
    - Delimiter run length enforcement: code spans open with $K$ backticks and close strictly with $K$ backticks, supporting nested backticks (`` `foo` ``) and space trimming rules.
    - Paragraph boundary reset: unclosed backticks reset upon encountering empty lines (`\n\n`), preventing backtick parity inversion from swallowing subsequent paragraphs.
  - **CommonMark 4.5 Fenced Code Blocks**:
    - Supported variable fence lengths ($\ge 3$ backticks), allowing outer 4-backtick code fences to encapsulate inner 3-backtick code blocks without premature closure.
    - Automatic EOF document closure for unclosed code fences.
  - **Strict DOM Hierarchy & Block Isolation**:
    - Block elements (`<blockquote>`, `<pre><code>`) are lexed, internally formatted, and shielded into UUID placeholders prior to inline paragraph formatting, guaranteeing that inline tags (`<b>`, `<i>`, `<s>`) never intersect or improperly wrap block elements.
    - Excluded spoiler syntax (`||...||`) from false-positive Markdown table detection.
  - **UTF-16 Metric for Telegram Bot API Limits**:
    - Upgraded `SplitHTMLChunks` and `splitOversizedParagraph` to measure chunk sizes using UTF-16 code units (`len(utf16.Encode([]rune(s)))`), preventing Bot API length rejections caused by 4-byte astral runes and emoji surrogate pairs (🫥, 🚀).
  - **Stream OpSec & Reasoning Tag Neutralization**:
    - Preserved raw reasoning tags (`<thought>`, `<think>`) within code spans and code blocks without false extraction.
    - Unclosed reasoning tags in text are safely escaped to `&lt;thought&gt;` and `&lt;think&gt;` without leaking or triggering blockquote conversion.
  - **Test Suite**:
    - Implemented comprehensive `TestMarkdownToTelegramHTML_RobustSuite` covering all 12 edge case scenarios under `go test -race ./...`.

### Thought Tags Code Shielding & Stream Truncation Prevention (Issue #351)
- **Code Shielding First & Strict Tag Matching**:
  - Reordered Markdown parsing in `formatters.go::MarkdownToTelegramHTML`: fenced code blocks (` ``` `) and inline code (`` ` ``) are now shielded into UUID placeholders at Step 0, before evaluating reasoning tags (`<think>`, `<thinking>`, `<thought>`).
  - Strict closed tag enforcement: replaced greedy `(?:</thought>|$)` with strict closing tag requirements (`(?is)<thought>(.*?)</thought>|<think>(.*?)</think>|<thinking>(.*?)</thinking>`), eliminating premature message truncations and severed Markdown formatting.
  - Safely treat unclosed opening tags as plain text, escaping `<` and `>` into `&lt;` and `&gt;` without corrupting Telegram HTML parsing or dropping trailing response content.
  - Replaced dynamic in-loop and in-call `regexp.MustCompile` with package-level precompiled regex variables in `formatters.go`, eliminating memory allocations in the streaming hot path.
  - Implemented code shielding parity in `rich_message.go`: `ExtractThinkingAndMarkdown` and `HasThoughts` now shield fenced and inline code blocks, preventing thought tags within code from being erroneously extracted or triggering Tier 2 delivery routing.
  - Added comprehensive L1/L2 regression tests in `formatters_test.go` and `rich_message_test.go` (`TestMarkdownToTelegramHTML_ThoughtTagsShielding`, `TestHasThoughts_CodeShieldingAndUnclosed`).

### Clean Sticker Image & Semantic De-hijacking (Issue #347)
- **Pure Image Sticker Routing**:
  - Removed synthetic emoji context injection (`[Пользователь отправил стикер: %s]`) in `handlers.go`, eliminating semantic hijacking of agent decision-making.
  - Stickers are now routed as pure image attachments (`[Attached File: file://...]`), allowing the model to naturally inspect visual triggers and custom button graphics via multimodal sensors (`view_file`).
  - Preserved authentic accompanying user text/captions without synthetic emoji wrapping.
  - Implemented dynamic extension detection (`.webm` for video stickers, `.tgs` for animated stickers, `.webp` for static stickers) based on actual Telegram Bot API file paths in `downloadTelegramMedia`.
  - Added comprehensive test coverage in `handlers_test.go`: `TestExtractInboundPayload` (clean text assertion for all sticker types), `TestHandleUpdate_StickerVideo` (`.webm` download and storage verification), and `TestSticker_PromptFormatting_CleanImageWithoutEmoji`.

### Tri-Modal Response Delivery Architecture (Issue #345)
- **Tri-Modal Dispatch Routing (`sendAdaptiveResponse`)**:
  - Implemented three-tier response routing to permanently eliminate routine HTML chunk splitting spam (`SplitHTMLChunks` cascade):
    - **Tier 1 (Classic Bubble)**: Conversational responses under 3000 UTF-8 runes (without tables or thinking tags) route to single HTML `sendMessage` / `editMessageText`, preserving natural chat dialogue.
    - **Tier 2 (Rich Article)**: Responses between 3000 and 32,768 runes, or containing Markdown tables (`HasMarkdownTable`), or containing LLM reasoning blocks (`HasThoughts`), route to monolithic `sendRichMessage`.
    - **Tier 3 (Markdown Artifact)**: Extreme payloads exceeding 32,768 runes route to a 2500-rune safe preview summary via `sendRichMessage` + unclipped `.md` file saved to `scratch/downloads/` with `0600` permissions and uploaded as a Telegram Document (`agent_response.md`).
  - Added `TruncateMarkdownSafely`: rune-safe Markdown truncation with proportional lookback windows (paragraph, newline, space) and automatic closure of unclosed fenced code blocks (`\n```\n`).
  - Guaranteed atomic deletion of streaming draft messages (`activeMsgID`) upon Tier 2 and Tier 3 dispatch.
  - Eliminated the legacy `strings.Contains(text, "⏳")` footgun in `handlers.go`.
  - Implemented workspace-scoped artifact storage (`sendAdaptiveResponseWithWorkspace`) with multi-agent pool isolation (fallback to `common/scratch/downloads`, 0 hardcoded bot names).
  - Hardened tier classification in `DetermineDeliveryTier` to check rendered HTML length against `ClassicMessageLimit` (4000), preventing message split cascades on entity-heavy Markdown.
  - Routed non-classic tiers in `sendChunk(messageID == 0)` to `sendAdaptiveResponse` to eliminate Tier 3 bypass.
  - Added artifact retention rotation (`RotateArtifactFiles`), pruning files older than 24h and keeping at most 20 recent artifacts to prevent disk exhaustion.
  - Added comprehensive L2 unit tests: `TestDelivery_Tier1_ClassicBubble`, `TestDelivery_Tier2_RichArticle`, `TestDelivery_Tier3_ExtremePayloadMarkdownArtifact`, `TestDelivery_Tier3_MarkdownTruncationIntegrity`, `TestArtifactSaveDir_Isolation_NoHardcodedBotName`, `TestDetermineDeliveryTier_HTMLLengthExpansion`, `TestSendChunk_Tier3_Routing_MessageIDZero`, `TestRotateArtifactFiles`, and `TestSession_FinalizeTurn_Tier3ExtremePayload` (race-detector verified).

### Master-Detail Ergonomic Dashboard & Zero Footgun UI (Issue #343)
- **Two-Tiered Master-Detail Dashboard Architecture (Issue #343)**:
  - Eliminated the 30-button wide row wall and horizontal text truncation in `/accounts` by splitting account management into Master (Overview) and Detail (Account Card) views.
  - Implemented 2-per-row compact grid layout for account selection in `formatAccountsDashboard` (`[ 🟢 ID 🔒 ] [ ⚡ ID • ]`) with dynamic state badges (`🟢 Active`, `⚡ In-Use`, `⏳ Cooldown`, `🧊 Frozen`, `🔴 Expired`) and chat relationship indicators (`🔒 Pinned`, `• Active`).
  - Added dedicated global action rows in the master dashboard: `[🔓 Unpin (Enable Auto-Pool)]` (dynamic chat unpin) alongside `[🔄 Refresh Quotas]` and `[📥 Ingest Current Login]`.
  - Implemented interactive in-place `formatAccountCard` (`acc:manage:<id>`): displays comprehensive account dossier (email, home directory, live status, Gemini/Claude quota windows with UTC reset times, active turn counts, total errors, and last used timestamp).
  - Provided full-width (1 button per row) action palette on account cards: `Switch to this Account`, `Pin to this Chat (Sticky Mode)` / `Unpin`, `Freeze Account` / `Unfreeze Account`, `Delete Account from Pool`, and `[🔙 « Back to Account List]`.
  - Guaranteed **Zero Footgun & Nil-Safety**: dangerous actions (`Delete`, `Freeze`) quarantined inside detail cards; missing/deleted accounts safely intercepted with callback alerts and automatic dashboard refreshes without nil pointer dereference.
  - Implemented context-aware deletion cancellation (`acc:del_cancel:<id>`): returning from deletion confirmation returns directly to the specific account card.
  - Softly suppressed harmless `Bad Request: message is not modified` API errors across in-place navigation transitions (`manage`, `back`, `del_cancel`).
  - Added comprehensive unit test suite in `account_handlers_test.go` (`TestAccountsDashboard_MasterDetailGridRendering`, `TestAccountCard_ButtonsAndQuotaFormatting`, `TestAccountCallback_NavigationMasterDetailBack`, `TestAccountCallback_ManageNonExistentAccountSafety`).

### Account Lifecycle Management: Freeze, Unfreeze, Delete & Administrative Immunity (Issue #339)
- **Profile Lifecycle Supervision (Issue #339)**:
  - Added `StateFrozen` state to `AccountState` with badge `🧊 Frozen (Administrative Hold)`.
  - Implemented `FreezeAccount`, `UnfreezeAccount`, and `DeleteAccount` in `account_pool.go` with thread-safe atomic state transitions and persistence (`SaveState`).
  - Enforced **Absolute Administrative Immunity**: frozen accounts are completely bypassed during rotation in `AcquireAccount`, `SwitchAccount`, and `PinAccount`, and are strictly immune to automatic background cooldown expiration (`StartBackgroundReaper`) or quota probe recovery (`FetchAccountQuotas`).
  - Implemented session eviction and child CLI process reaping via `ResetAccountSessions(accountID)` in `account_handlers.go`, guaranteeing that freezing or deleting an account terminates active subprocesses immediately.
  - Implemented runtime session failover in `session.go`: re-acquires a fresh healthy account if a bound session account is frozen or in cooldown upon launch.
  - Hardened path traversal security in `DeleteAccount`: verifies that `acc.HomeDir` resides strictly within `p.accountsDir` via `filepath.Rel` and `filepath.Clean`, rejecting escaping paths (`..`), root (`/`), or the pool directory itself.
  - Ensured **Zero Central Storage Loss**: directory deletion in `DeleteAccount(..., purgeStorage=true)` only unlinks symlinks to shared central resources (`conversations`, caches), preserving central host data.
  - Added Telegram commands `/accounts freeze <id>`, `/accounts unfreeze <id>`, and `/accounts delete <id> [purge]`, and updated `/accounts help`.
  - Enhanced interactive Telegram dashboard with `[❄️ Freeze]`, `[🧊 Unfreeze]`, and `[🗑️ Delete]` buttons, including an inline two-step confirmation dialog with Keep Storage vs. Purge Storage choices and Cancel support.
  - Added comprehensive unit test suite in `account_pool_test.go` covering lifecycle transitions, administrative immunity, reaper/quota bypass, path traversal guards, central storage preservation, session reacquisition, CLI commands, and callback queries.

### Rich Message Deduplication & Streaming Finalization Race Fix (Issue #340)
- **Elimination of Race Condition & Duplicate Messages in Rich Message Finalization (Issue #340)**:
  - Implemented **Early Disarm** in `session.go`: atomically zero out `s.ActiveMessageID = 0` under `s.mu.Lock()` *before* invoking `sendAdaptiveResponse`, preventing concurrent streaming throttler ticks from editing or sending obsolete draft messages.
  - Guaranteed **Active Turn Immunity** in `session.go`: maintained `s.ActiveTurnStart` active throughout the entire adaptive dispatch and artifact delivery phase, protecting the in-flight turn from aggressive memory GC eviction until finalization finishes.
  - Hardened `sendChunk` in `handlers.go`: suppressed fallback to `newMsg` when Telegram returns `Bad Request: message to edit not found`, `message can't be edited`, or `message is not modified`, completely eliminating ghost/duplicate message delivery. Fallback to `newMsg` is strictly reserved for genuine markup entity formatting errors.
  - Added comprehensive L1 unit tests in `handlers_test.go` (`TestStrictTelegram_MessageToEditNotFoundSuppressed` and updated `TestStrictTelegram_EditFailureTriggersNewMessageFallback`).
  - Added race-detector verified tests in `session_test.go` (`TestSession_StreamingThrottler_RaceWithRichMessageFinalization` and `TestSession_FinalizeTurn_EarlyDisarmAndActiveTurnProtection`) confirming 0 data races, 0 duplicate messages, and 100% active turn protection.

### Telegram RichMessage & Native Tables Support (Issue #336)
- **Telegram RichMessage Format & Native Tables (Issue #336)**:
  - Implemented `InputRichMessage` and `RichBlockThinking` structures in `rich_message.go`.
  - Added `sendRichMessage` Bot API endpoint client method sending monolithic articles up to 32,768 UTF-8 characters.
  - Implemented `HasMarkdownTable` table detector and `ExtractThinkingAndMarkdown` CoT parser.
  - Implemented `sendAdaptiveResponse` intelligent router with automatic fallback to classic `SplitHTMLChunks(4000)` + `sendMessage` on any Bot API failure (0% message loss).
  - Enforced strict UTF-8 rune limit of 32,768 characters via `SanitizeRichMessageText`.
  - Extended `MarkdownToTelegramHTML` to support `<thought>` blocks in classic mode.
  - Added comprehensive test suite `rich_message_test.go` covering serialization, table detection, thinking extraction, 32K sanitization, adaptive routing, and graceful degradation fallback.

### Documentation & Codebase Invariants Synchronization
- **Codebase-Wide Documentation Re-alignment**:
  - Synchronized `README.md` test coverage badge to verified benchmark of `81.0%` (2289/2827 statements).
  - Documented all 14 targets in `Makefile` and clarified `/workspace` security sandboxing boundaries.
  - Corrected `ACCOUNTS_DIR` (`/etc/antigravity-bot/accounts`) and `AGY_BINARY` (`PATH` resolution priority) in `README.md`.
  - Added Section 13 to `docs/ARCHITECTURE.md` detailing Context Preservation & Session Desync Detection Architecture (#303), complete with sequence diagram and database quarantine mechanisms.
  - Synchronized SQLite schemas (`session_history` unique constraints and `is_orphaned` column; `users` with `is_first_start`) and Webhook Guard function names (`clearWebhookOnStartup`, `recoverFromWebhookConflict`, `getUpdatesWithRecovery`) in `docs/ARCHITECTURE.md`.
  - Rebranded `.github/SECURITY.md` to `antigravity-telegram-agent` and updated supported versions matrix.
  - Hardened local development setup (`ALLOW_DOTENV=1` / `make run`) and test commands in `CONTRIBUTING.md`.
  - Added `session_context_resets_total` to `docs/HARVESTER_RUNBOOK.md` metrics catalog.

### CI/CD Resilience & Repository Hygiene (Issue #301)
- **CI Tooling Hardening & Git Hygiene (Issue #301)**:
  - Added retry loops for static analysis tools installation (`staticcheck`, `gosec`, `govulncheck`) and `govulncheck` scans in `.github/workflows/ci.yml` to eliminate transient network failures.
  - Added job timeout (`timeout-minutes: 5`) and workflow concurrency cancellation to `.github/workflows/secret-scan.yml`.
  - Audited and pruned all 19 stale remote-tracking refs and merged local branches.
  - Synchronized SQLite schemas and webhook guard function names in `docs/ARCHITECTURE.md`.
  - Synchronized `README.md` test coverage badge to verified 81.0%, corrected path resolution descriptions, and documented all Makefile targets.

### Context Preservation & Session Desync Detection (Issue #303)
- **Silent Fallback & Desync Prevention for `--conversation` (Issue #303)**:
  - Detected and alerted on runtime conversation desync in `readStdoutLoop` when `agy` CLI silently rejects `--conversation` due to missing disk state and generates a fresh UUID.
  - Added `session_context_resets_total` Prometheus counter with `bot` label and integrated with `RegisterEngineMetrics`.
  - Added dynamic schema migration for `is_orphaned` column in `session_history` table and automated `markSessionOrphaned` flagging.
  - Filtered orphaned conversations out of `/resume` inline session history list (`handleResumeCommand`).
  - Delivered real-time transparent user notification in Telegram (prepending to stream buffer during turns or sending immediate HTML notice) explaining previous context loss and identifying fresh conversation ID.
  - Guarded callback resumption in `handleCallbackQuery` to warn users when attempting to resume a session whose context is missing on disk.
  - Added comprehensive unit test suite `session_context_desync_test.go` covering clean start non-regression, stream buffering, orphaned exclusion, and alert notifications.

### Bug Fixes & Multi-Account Architecture (Issue #302)
- **Dangling Conversations Symlink Self-Healing & System Home Resolution (Issue #302, PR #303)**:
  - Fixed `getSystemBaseHome()` and `getSharedConversationsDir()` in `account_pool.go` to prioritize `SYSTEM_HOME` and exclude systemd service account directories (`/accounts/`), eliminating path pollution and binding to ephemeral daemon homes.
  - Hardened `ensureSymlink()` to guarantee creation of target directories (`os.MkdirAll`) prior to symlinking, and automatically detect and prune broken/dangling symlinks via `os.Stat()`.
  - Upgraded `EnsureSharedAccountDirectories()` to auto-heal existing broken conversation symlinks across all account profiles, resolving Linux VFS `EEXIST` (`file exists`) errors in upstream `agy` stager (`stager.go:117`) and preventing multi-turn conversation context loss.
  - Added unit test suite (`TestEnsureSharedAccountDirectories_DanglingSymlinkRecovery`, `TestEnsureSharedAccountDirectories_SystemHomeFallback`, `TestEnsureSymlink_DanglingSymlinkSelfHealing`) verifying automatic recovery and path resolution.

### Reliability & Autonomous Resource Management (Issue #304)
- **Memory-Aware Session GC & OOM Prevention (Issue #304)**:
  - Implemented dynamic memory pressure detection via `/proc/meminfo` (`getSystemMemoryStats`).
  - Added aggressive session idle eviction (20m threshold) when system memory pressure exceeds 75% RAM usage or available RAM drops below 1.5GB.
  - Reduced default `SESSION_MAX_IDLE` from 4h to 2h, and accelerated session GC interval to 1m.
  - Guaranteed absolute in-flight turn immunity (`ActiveMessageID != 0` or `!ActiveTurnStart.IsZero()`), protecting active streaming turns from premature eviction.
  - Validated zero context loss on idle eviction due to disk-backed conversation persistence and seamless CLI resurrection.

### Reliability & Multi-Account Quota Management (Issue #298)
- **Disabled Quota Flag Parsing & 429 Failover Recovery (Issue #298)**:
  - Added `Disabled` boolean deserialization in `ModelQuota` and `ParseUsageJSON` to capture `disabled: true` when Antigravity CLI exhausts weekly limits.
  - Normalized `RemainingFraction` to `0.0` when a quota bucket is marked disabled or weekly quota hits 0%, inheriting `reset_time` from the weekly limit window.
  - Excluded accounts with disabled or exhausted Gemini quota from `AcquireAccount` candidate pool and sticky session retention, avoiding erroneous selections.
  - Updated Telegram `/accounts` dashboard formatting to explicitly display `disabled` (e.g., `Gemini: 5h disabled • 7d 0%`) instead of misleading `100%`.
  - Updated auto-failover and cooldown calculation in `session.go` and `account_pool.go` to eliminate the 30-second backoff loop on disabled models.

## [1.3.0] - 2026-09-20

### Security & Reliability (Incident #294 Resolution)
- **Defensive deleteWebhook Guard & 409 Conflict Auto-Recovery (Issues #294, #295, PR #296)**:
  - Implemented Layer 1 startup `deleteWebhook` guard (`webhook_guard.go`) purging stale or unauthorized external webhooks across all swarm bots before launching Long Polling workers.
  - Implemented Layer 2 runtime auto-recovery in polling loop intercepting HTTP 409 Conflict with automatic webhook teardown and retry backoff, preventing infinite loop crashes and bot outages.
  - Enforced explicit `AllowedUpdates` registration (`message`, `edited_message`, `channel_post`, `edited_channel_post`, `callback_query`), ensuring zero dropped inline button callbacks and securing interactive UI menus.
  - Added full unit test suite with mocked Telegram Bot API server (`webhook_guard_test.go`) covering all recovery paths and edge cases.
- **Automated Gitleaks CI Pipeline & Secret Hardening (Issue #273)**:
  - Integrated automated Gitleaks workflow (`.github/workflows/secret-scan.yml`) across all pushes and pull requests.
  - Hardened `.gitignore` and `.gitleaks.toml` rules covering `.secrets*`, `*.token`, `tokens/`, `credentials.json`, and database state files.
- **Child Process Isolation & Secret Leakage Elimination (Issues #281, #282, #283)**:
  - Replaced `os.Environ()` denylist inheritance with strict `buildChildEnv` allowlist (`LANG`, `LC_ALL`, `TZ`, `TERM`, `PATH`, `SYSTEM_HOME`, `USER`, `TMPDIR`, proxies), preventing supervisor credentials (`BOT_TOKENS`, `ALLOWED_ADMIN_IDS`, `ELEVENLABS_API_KEY`) from bleeding into child processes.
  - Suppressed phantom error dispatches in `readStdoutLoop` on idle session teardown (`stream input cancelled: context canceled`) during supervisor restarts.

### Reliability & Lifecycle
- **Graceful SIGTERM Teardown & Maintenance Notice (Issues #284, #290)**:
  - Delivered dedicated user-facing maintenance disclaimer upon daemon shutdown/restart.
  - Suppressed mid-turn SIGTERM agent error dispatches so routine server maintenance does not generate false alarms.
- **Projects Directory Resolution & Workspace Sandboxing (Issues #288, #289)**:
  - Resolved `PROJECTS_DIR` from `SYSTEM_HOME` to support multi-account isolation and arbitrary system users.
  - Enhanced workspace-scoped artifact sandboxing and symlink traversal checks.

### UI/UX & Localization
- **Strict English UI Localization (Issues #291, #292)**:
  - Standardized compaction advice prompts, stream disruption warnings, and artifact captions to English across all bot responses.

### Testing & Infrastructure
- **Go 1.18+ Native Fuzzing Suite (Issue #276)**:
  - Added continuous native fuzz tests for Telegram update dispatchers, TTS cleaners, and Harvester Markdown sanitizers.
- **Zero-Leak Goroutine Verification (Issue #275)**:
  - Integrated `uber-go/goleak` guards across test suites and background cleanup workers.
- **Interactive Account UI & BOLA Authorization Guards (Issue #274)**:
  - Added comprehensive unit tests for account callback routing, Broken Object Level Authorization (BOLA) prevention, and quota mock HTTP recovery.

### Portability & Deployment
- **Portability & Public Release Gate (Issues #277, #278, #280)**:
  - Eliminated hardcoded host paths across path sanitizers.
  - Dynamic `agy` binary discovery resolving through `SYSTEM_HOME` and `PATH`.
  - Rebranded project documentation and repository identifiers to Antigravity Telegram Agent.

### Added
- **Session Artifacts Post-Mortem Exhumation Hook (Issue #269)**:
  - Implemented automatic exhumation hook on `[🆕 New Session]` and `/clear` in accordance with the Session Artifacts Manifesto (`SESSION_ARTIFACTS_MANIFESTO.md`).
  - Added 4-sieve anti-garbage filtering in `pkg/harvester/exhumation.go` (strict `.md`, maturity threshold `>= 200B`, ignore `scratch/`, ignore service masks `*.tmp`, `*.bak`, `*.orig`, `draft_*`, `test_*`, and require `UserFacing == true`).
  - Added No-ZIP Telegram delivery: directly delivers exhumed Markdown documents with emoji classification and clean captions.
  - Implemented artifact alienation (`Move, not Copy`): moves documents to `ecosystem-docs/inbox/<YYYY-MM-DD>_<bot_name>_<slug>.md` and cleans up sidecar `.metadata.json` so no orphaned files remain in session storage (`agy-harvester doctor` clean).
  - Extended Secret Shield to redact Google API keys (`AIza...`) and JWT tokens.
- **Harvester Subsystem & CLI (`agy-harvester`) (Issues #244, #245, #246, #247, #248, #258, #259, #260)**:
  - **`pkg/harvester`**: Added core streaming transcript parser, document taxonomy classifier, secret shield sanitizer, and ZIP bundler with `manifest.json`.
  - **`cmd/agy-harvester`**: Added standalone CLI for headless environment harvesting with `scan`, `extract`, and `doctor` commands.
  - **Telemetry**: Integrated Prometheus exporter metrics for Harvester batch operations.
  - **Documentation**: Added operational runbook at `docs/HARVESTER_RUNBOOK.md`.
- **Dashboard & Session UX Enhancements**:
  - Added active account indicator (`👤 Account: <id>`) to the `/start` terminal card with sticky lock indicator (`🔒`) when pinned.
  - Replaced ambiguous `[🧼 Clear]` button with `[🆕 New Session]` in the `/start` inline keyboard and updated eviction notice UX.
  - Added quick-access `[👥 Accounts]` button directly into the `/start` keyboard with callback routing (`cmd:accounts`).
  - Added transcript compaction & stream resilience reminder (`getCompactionHint`): automatically appends an actionable recommendation to export and restart the session when `transcript.jsonl >= 500 KB` or upon mid-turn stream disruptions.

### Fixed
- **Stream Recovery Infinite Loop & Process Race Prevention**:
  - Enforced `maxTurnFailovers = 1` and `maxTurnStreamRetries = 2` to prevent cascading infinite account rotations and account pool thrashing during upstream Google Cloud stream interruptions.
  - Retained `StreamRetries` across failover rotations rather than resetting to 0, bounding the total recovery budget per user turn.
  - Introduced `cmdEpoch` generation counter in `AgySession` and `cmdWait`, ensuring that superseded subprocesses cannot corrupt active session buffers or send spurious restart notices to Telegram during recovery handoffs.

### Added
- **FinOps CI/CD Optimization (Issue #251)**:
  - Added workflow `concurrency` cancellation (`cancel-in-progress: true`) to `.github/workflows/ci.yml`.
  - Added `paths-ignore` for documentation and markdown files (`**.md`, `docs/**`, `.gitignore`, `LICENSE`).
  - Added caching for SAST tools (`staticcheck`, `gosec`, `govulncheck`) to avoid recompilation on every run.
- **Documentation & Security Policy**:
  - Added `SECURITY.md` defining vulnerability disclosure standards.
  - Added Multi-Account Pool architecture and `/accounts` command reference to `README.md` and `docs/ARCHITECTURE.md`.
  - Updated statement coverage metrics to reflect real benchmark (75.4%).

---

## [1.2.0] - 2026-09-10

### Added
- **Multi-Account Quota Pool (`account_pool.go`, `account_handlers.go`)**:
  - Dynamic discovery and health probing across `/etc/antigravity-bot/accounts/*/`.
  - Seamless stream interruption auto-failover to next healthy account (#254, #256).
  - Background quota health checks with automatic unbanning of recovered accounts.
  - Deduplicated Go build/mod, npm, and pip caches across account home directories (#250).
- **Session Eviction & Safe Parking**:
  - Cold session eviction on `/clear` and 4-hour idle session GC (#252).
  - Cross-turn Safe Parking protocol saving active state before cooldown.

### Fixed
- **Usage Command Root Leak**: Prevented `/usage` from silently executing under host `/root` when all pool accounts are resting.
- **Subprocess WaitDelay**: Enforced `WaitDelay: 5 * time.Second` and clean background reaper cancellation on shutdown (#242).

---

## [1.1.0] - 2026-09-08

### Added
- **Hybrid TTS Audio Engine (`tts.go`)**:
  - Primary Edge-TTS with Piper CPU fallback and ElevenLabs multi-key rotation.
- **Autonomous Subprocess Watchdog (`subprocess_watchdog.go`)**:
  - Two-phase `SIGCONT` + `SIGKILL` reaper eliminating `SIGTTIN`/`SIGTTOU` State T deadlocks.
- **Extended Inactivity Turn Watchdog**:
  - 15-minute inactivity window and 45-minute hard turn safety ceiling.

---

## [1.0.0] - 2026-09-01

### Added
- Initial release of the Pure Go Antigravity Telegram Bot Agent.
- Asynchronous 1200ms coalescing stream throttler.
- Mutex-decoupled non-blocking I/O and process group (`pgid`) isolation.
- SQLite WAL mode session persistence.
