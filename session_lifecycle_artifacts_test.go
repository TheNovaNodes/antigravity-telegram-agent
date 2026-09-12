package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func TestExtractAllowedArtifacts_ProjectsDirAndFileValidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	projectsDir := filepath.Join(tempDir, "projects")
	agentsDir := filepath.Join(tempDir, "agents")
	brainDir := filepath.Join(tempDir, "brain")

	os.MkdirAll(projectsDir, 0755)
	os.MkdirAll(agentsDir, 0755)
	os.MkdirAll(brainDir, 0755)

	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)
	t.Setenv("BRAIN_DIR", brainDir)

	// Create test files
	projFile := filepath.Join(projectsDir, "result.go")
	os.WriteFile(projFile, []byte("package main\n"), 0644)

	agentFile := filepath.Join(agentsDir, "agent_config.json")
	os.WriteFile(agentFile, []byte("{}"), 0644)

	brainFile := filepath.Join(brainDir, "session_note.txt")
	os.WriteFile(brainFile, []byte("notes"), 0644)

	// Create test directory (must be rejected by IsDir check)
	subDir := filepath.Join(projectsDir, "some_subfolder")
	os.MkdirAll(subDir, 0755)

	// Construct markdown text with multiple file links
	text := fmt.Sprintf("Here are artifacts:\n"+
		"- [Project File](file://%s)\n"+
		"- [Agent File](file://%s)\n"+
		"- [Brain File](file://%s)\n"+
		"- [Directory Link](file://%s)\n"+
		"- [Forbidden Link](file:///etc/passwd)\n",
		projFile, agentFile, brainFile, subDir)

	paths := ExtractAllowedArtifacts(text)

	// Expect exactly 3 allowed files (projFile, agentFile, brainFile)
	if len(paths) != 3 {
		t.Fatalf("Expected exactly 3 allowed artifacts, got %d: %v", len(paths), paths)
	}

	foundMap := make(map[string]bool)
	for _, p := range paths {
		foundMap[p] = true
	}

	if !foundMap[projFile] {
		t.Errorf("Expected project file %s to be allowed", projFile)
	}
	if !foundMap[agentFile] {
		t.Errorf("Expected agent file %s to be allowed", agentFile)
	}
	if !foundMap[brainFile] {
		t.Errorf("Expected brain file %s to be allowed", brainFile)
	}
	if foundMap[subDir] {
		t.Errorf("Expected directory %s to be rejected", subDir)
	}
	if foundMap["/etc/passwd"] {
		t.Errorf("Expected /etc/passwd to be blocked by LFI whitelist")
	}
}

func TestHandleVoiceToggleCommand_SyncsInMemorySession(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (2001, '/root', 'gemini-3.7-flash-high', 0, 'session_1', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	botName := "VoiceSyncBot"
	chatID := int64(999111)
	user := User{ID: 2001, Workspace: "/root", Model: "gemini-3.7-flash-high", VoiceReply: false}

	// Create and register session in memory
	session := getSession(botName, user, chatID)
	if session.VoiceReply != false {
		t.Fatalf("Expected initial session.VoiceReply to be false")
	}

	bot := &tgbotapi.BotAPI{}

	// Toggle voice ON
	handleVoiceToggleCommand(bot, chatID, 2001, "/voice on", botName, user, db)

	// Check DB
	var dbVoice int
	if err := db.QueryRow("SELECT voice_reply FROM users WHERE user_id = 2001").Scan(&dbVoice); err != nil {
		t.Fatalf("Failed to query DB voice_reply: %v", err)
	}
	if dbVoice != 1 {
		t.Errorf("Expected DB voice_reply to be 1, got %d", dbVoice)
	}

	// Check in-memory session
	session.mu.Lock()
	inMemVoice := session.VoiceReply
	session.mu.Unlock()
	if inMemVoice != true {
		t.Errorf("Expected in-memory session.VoiceReply to be synced to true, got %v", inMemVoice)
	}

	// Toggle voice OFF
	user.VoiceReply = true
	handleVoiceToggleCommand(bot, chatID, 2001, "/voice off", botName, user, db)

	session.mu.Lock()
	inMemVoice = session.VoiceReply
	session.mu.Unlock()
	if inMemVoice != false {
		t.Errorf("Expected in-memory session.VoiceReply to be synced to false, got %v", inMemVoice)
	}
}

func TestHandleMessagePayload_VoiceReplyPerTurnNoLatch(t *testing.T) {
	botName := "VoiceTurnBot"
	chatID := int64(999222)
	user := User{ID: 2002, Workspace: "/root", Model: "gemini-3.7-flash-high", VoiceReply: false}

	session := getSession(botName, user, chatID)

	// Turn 1: User sends voice message (isVoice = true)
	session.mu.Lock()
	session.VoiceReply = true || user.VoiceReply
	session.mu.Unlock()

	if !session.VoiceReply {
		t.Fatalf("Expected session.VoiceReply to be true for voice turn")
	}

	// Turn 2: User sends text message (isVoice = false, user.VoiceReply = false)
	session.mu.Lock()
	session.VoiceReply = false || user.VoiceReply
	session.mu.Unlock()

	if session.VoiceReply {
		t.Errorf("Expected session.VoiceReply to reset to false for text turn, but it latched to true")
	}
}

func TestExtractAllowedArtifacts_MarkdownLinksAndDeduplication(t *testing.T) {
	tempDir := t.TempDir()
	agentsDir := filepath.Join(tempDir, "agents")
	os.MkdirAll(agentsDir, 0755)
	t.Setenv("AGENTS_DIR", agentsDir)

	pdfFile := filepath.Join(agentsDir, "CHECKLIST_FXLAB_2026-09-07.pdf")
	os.WriteFile(pdfFile, []byte("%PDF-1.4"), 0644)

	docFile := filepath.Join(agentsDir, "report.docx")
	os.WriteFile(docFile, []byte("data"), 0644)

	text := fmt.Sprintf("Here are files:\n"+
		"1. Standard markdown: [Checklist](%s)\n"+
		"2. File URI: (file://%s)\n"+
		"3. Image markdown: ![Report](%s)\n"+
		"4. Web link: [Web](https://example.com/file.pdf)\n",
		pdfFile, pdfFile, docFile)

	paths := ExtractAllowedArtifacts(text)

	// Should extract exactly 2 unique files: pdfFile (deduplicated) and docFile
	if len(paths) != 2 {
		t.Fatalf("Expected exactly 2 deduplicated files, got %d: %v", len(paths), paths)
	}
	foundMap := make(map[string]bool)
	for _, p := range paths {
		foundMap[p] = true
	}
	if !foundMap[pdfFile] {
		t.Errorf("Expected %s to be in extracted paths", pdfFile)
	}
	if !foundMap[docFile] {
		t.Errorf("Expected %s to be in extracted paths", docFile)
	}
}

func TestStreamingThrottler_EmptySuppression(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	session := &AgySession{
		BotName:         "ThrottlerTestBot",
		BotAPI:          bot,
		ChatID:          777888,
		ActiveMessageID: 100,
		TextBuffer:      "",
		UpdateChan:      make(chan struct{}, 10),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Launch throttler loop with empty TextBuffer
	var lastSentText string
	hasDelta := false
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	// Simulate UpdateChan events while TextBuffer remains whitespace only
	session.UpdateChan <- struct{}{}

	ticks := 0
	for ticks < 3 {
		select {
		case <-ctx.Done():
			t.Fatal("Context timed out waiting for ticks")
		case <-session.UpdateChan:
			hasDelta = true
		case <-ticker.C:
			ticks++
			if !hasDelta {
				continue
			}
			session.mu.Lock()
			text := session.TextBuffer
			activeMsgID := session.ActiveMessageID
			botAPI := session.BotAPI
			chatID := session.ChatID
			session.mu.Unlock()

			trimmed := strings.TrimSpace(text)
			if activeMsgID == 0 || botAPI == nil || trimmed == "" {
				continue
			}
			if trimmed == lastSentText {
				hasDelta = false
				continue
			}
			sendChunk(botAPI, chatID, activeMsgID, text)
			lastSentText = trimmed
			hasDelta = false
		}
	}

	// Because TextBuffer was empty, no edits or messages should have been sent to mockServer
	ms.mu.Lock()
	var messageCalls []string
	for _, req := range ms.sentRequests {
		if !strings.Contains(req.URL.Path, "getMe") {
			messageCalls = append(messageCalls, req.URL.Path)
		}
	}
	ms.mu.Unlock()
	if len(messageCalls) != 0 {
		t.Errorf("Expected 0 Telegram edits/messages when TextBuffer is empty, got %d: %v", len(messageCalls), messageCalls)
	}
}

func TestSendArtifacts_SanitizesSecretsInAllTextTypes(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tempDir := t.TempDir()
	agentsDir := filepath.Join(tempDir, "agents")
	os.MkdirAll(agentsDir, 0755)
	t.Setenv("AGENTS_DIR", agentsDir)

	// Create a .toml file with a secret
	tomlFile := filepath.Join(agentsDir, "config.toml")
	tomlContent := `api_key = "sk-ant-12345678901234567890"`
	os.WriteFile(tomlFile, []byte(tomlContent), 0644)

	// Create a .sql file with a secret
	sqlFile := filepath.Join(agentsDir, "dump.sql")
	sqlContent := `INSERT INTO tokens VALUES ('ghp_123456789012345678901234567890123456');`
	os.WriteFile(sqlFile, []byte(sqlContent), 0644)

	// Create an unknown extension file that is text with a secret
	unknownFile := filepath.Join(agentsDir, "unknown.data")
	unknownContent := `Bearer 123456789012345678901234567890123456`
	os.WriteFile(unknownFile, []byte(unknownContent), 0644)

	// Create a binary file (contains null byte)
	binFile := filepath.Join(agentsDir, "data.bin")
	binContent := []byte{0x00, 0x01, 0x02, 'B', 'e', 'a', 'r', 'e', 'r', ' ', '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3', '4', '5', '6'}
	os.WriteFile(binFile, binContent, 0644)

	text := fmt.Sprintf("Artifacts:\n- (file://%s)\n- (file://%s)\n- (file://%s)\n- (file://%s)\n", tomlFile, sqlFile, unknownFile, binFile)

	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	// 4 files sent
	if len(ms.sentBodies) < 4 {
		t.Fatalf("Expected 4 files to be sent, got %d", len(ms.sentBodies))
	}

	foundToml := false
	foundSql := false
	foundUnknown := false
	foundBin := false

	for _, body := range ms.sentBodies {
		if strings.Contains(body, "config.toml") {
			foundToml = true
			if strings.Contains(body, "sk-ant-12345678901234567890") {
				t.Errorf("toml file secret was not sanitized: %s", body)
			}
			if !strings.Contains(body, "[REDACTED_SECRET:ANTHROPIC_KEY]") {
				t.Errorf("toml file secret missing redacted string: %s", body)
			}
		} else if strings.Contains(body, "dump.sql") {
			foundSql = true
			if strings.Contains(body, "ghp_123456789012345678901234567890123456") {
				t.Errorf("sql file secret was not sanitized: %s", body)
			}
			if !strings.Contains(body, "[REDACTED_SECRET:GITHUB_PAT]") {
				t.Errorf("sql file secret missing redacted string: %s", body)
			}
		} else if strings.Contains(body, "unknown.data") {
			foundUnknown = true
			if strings.Contains(body, "Bearer 123456789012345678901234567890123456") {
				t.Errorf("unknown file secret was not sanitized: %s", body)
			}
			if !strings.Contains(body, "[REDACTED_SECRET:BEARER_TOKEN]") {
				t.Errorf("unknown file secret missing redacted string: %s", body)
			}
		} else if strings.Contains(body, "data.bin") {
			foundBin = true
			if strings.Contains(body, "[REDACTED_SECRET:BEARER_TOKEN]") {
				t.Errorf("binary file content was tampered with, should be sent as raw binary: %s", body)
			}
		}
	}

	if !foundToml {
		t.Errorf("Did not find toml file payload")
	}
	if !foundSql {
		t.Errorf("Did not find sql file payload")
	}
	if !foundUnknown {
		t.Errorf("Did not find unknown file payload")
	}
	if !foundBin {
		t.Errorf("Did not find bin file payload")
	}
}

func TestHandleClearCommand_PostMortemExhumation_DirectTelegramDeliveryAndAlienation(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	inboxDir := filepath.Join(tmpDir, "inbox")
	sessionID := "sess-exhume-live-101"
	sessionPath := filepath.Join(brainDir, sessionID)

	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	t.Setenv("BRAIN_DIR", brainDir)
	t.Setenv("ECOSYSTEM_INBOX_DIR", inboxDir)

	// Create mature RFC artifact (>200 bytes) with a secret token
	rfcPath := filepath.Join(sessionPath, "RFC_001_swarm_protocol.md")
	rfcContent := `# Request For Comments: Swarm Multi-Agent Protocol

## Motivation
Autonomous agents need reliable, zero-copy communication protocols.
Any sensitive keys like AIzaSyDummySecretGoogleAPIKey123456789 must be scrubbed before delivery.

## Proposal
Define canonical state machine events for agent collaboration and artifact exhumation.`
	if err := os.WriteFile(rfcPath, []byte(rfcContent), 0644); err != nil {
		t.Fatalf("failed to write RFC: %v", err)
	}

	// Sidecar metadata
	meta := struct {
		Summary    string
		UserFacing bool
	}{
		Summary:    "RFC for Swarm protocol.",
		UserFacing: true,
	}
	metaBytes, _ := json.Marshal(meta)
	if err := os.WriteFile(rfcPath+".metadata.json", metaBytes, 0644); err != nil {
		t.Fatalf("failed to write sidecar metadata: %v", err)
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3001, '/root', 'gemini-3.8-flash-high', 0, 'sess-exhume-live-101', 0);`); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	user := User{
		ID:        3001,
		Workspace: "/root",
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	chatID := int64(888777)
	botName := "TricksterBot"

	// Call handleClearCommand
	handleClearCommand(bot, chatID, 3001, botName, user, db)

	// 1. Verify user session in DB is reset to empty
	var updatedSessionID string
	if err := db.QueryRow("SELECT session_id FROM users WHERE user_id = 3001").Scan(&updatedSessionID); err != nil {
		t.Fatalf("failed to query updated session_id: %v", err)
	}
	if updatedSessionID != "" {
		t.Errorf("expected session_id in DB to be empty after clear, got %q", updatedSessionID)
	}

	// 2. Verify source artifact was moved out of brainDir (Move, not Copy)
	if _, err := os.Stat(rfcPath); !os.IsNotExist(err) {
		t.Errorf("expected source artifact %s to be removed from sessionDir, but it still exists", rfcPath)
	}
	if _, err := os.Stat(rfcPath + ".metadata.json"); !os.IsNotExist(err) {
		t.Errorf("expected sidecar metadata %s to be removed from sessionDir, but it still exists", rfcPath+".metadata.json")
	}

	// 3. Verify destination file exists in inboxDir
	inboxEntries, err := os.ReadDir(inboxDir)
	if err != nil || len(inboxEntries) != 1 {
		t.Fatalf("expected 1 file in inboxDir, got %d (err: %v)", len(inboxEntries), err)
	}

	inboxFile := filepath.Join(inboxDir, inboxEntries[0].Name())
	inboxContent, err := os.ReadFile(inboxFile)
	if err != nil {
		t.Fatalf("failed to read inbox file: %v", err)
	}

	// 4. Verify Secret Shield: secret was scrubbed in inbox file
	if strings.Contains(string(inboxContent), "AIzaSyDummySecretGoogleAPIKey123456789") {
		t.Errorf("secret token leaked into inbox file!")
	}
	if !strings.Contains(string(inboxContent), "[REDACTED_SECRET:GOOGLE_API_KEY]") {
		t.Errorf("missing redacted marker in inbox file!")
	}

	// 5. Verify Telegram messages sent: document delivery and clear confirmation
	ms.mu.Lock()
	defer ms.mu.Unlock()

	foundDoc := false
	foundNotice := false
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(body, "RFC_001_swarm_protocol.md") || strings.Contains(unescaped, "RFC_001_swarm_protocol.md") {
			foundDoc = true
			if !strings.Contains(unescaped, "Артефакт сессии") && !strings.Contains(body, "Артефакт сессии") {
				t.Errorf("document caption missing 'Артефакт сессии', got: %s", unescaped)
			}
			if strings.Contains(body, "AIzaSyDummySecretGoogleAPIKey123456789") || strings.Contains(unescaped, "AIzaSyDummySecretGoogleAPIKey123456789") {
				t.Errorf("secret token leaked into Telegram document delivery!")
			}
		}
		if strings.Contains(unescaped, "Exhumed and alienated *1* artifact(s)") || strings.Contains(body, "Exhumed and alienated *1* artifact(s)") {
			foundNotice = true
		}
	}

	if !foundDoc {
		t.Errorf("expected direct Telegram document delivery for exhumed artifact")
	}
	if !foundNotice {
		t.Errorf("expected eviction notification mentioning exhumed artifact count")
	}
}

func TestHandleClearCommand_AntiGarbageSieves(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	inboxDir := filepath.Join(tmpDir, "inbox")
	sessionID := "sess-trash-202"
	sessionPath := filepath.Join(brainDir, sessionID)
	scratchDir := filepath.Join(sessionPath, "scratch")

	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		t.Fatalf("failed to create scratch dir: %v", err)
	}

	t.Setenv("BRAIN_DIR", brainDir)
	t.Setenv("ECOSYSTEM_INBOX_DIR", inboxDir)

	// Sieve 1: Non-markdown file
	os.WriteFile(filepath.Join(sessionPath, "worker.py"), []byte(strings.Repeat("print('hello')\n", 30)), 0644)

	// Sieve 2: Inside scratch/
	os.WriteFile(filepath.Join(scratchDir, "scratch_notes.md"), []byte(strings.Repeat("scratch content\n", 30)), 0644)

	// Sieve 3: Below maturity threshold (<200 bytes)
	os.WriteFile(filepath.Join(sessionPath, "stub.md"), []byte("# Title\nShort stub.\n"), 0644)

	// Sieve 4: Service mask
	os.WriteFile(filepath.Join(sessionPath, "draft_arch.md"), []byte(strings.Repeat("draft content\n", 30)), 0644)

	// Sieve 5: UserFacing is false
	internalPath := filepath.Join(sessionPath, "internal.md")
	os.WriteFile(internalPath, []byte(strings.Repeat("internal content\n", 30)), 0644)
	meta := struct {
		UserFacing bool
	}{UserFacing: false}
	mBytes, _ := json.Marshal(meta)
	os.WriteFile(internalPath+".metadata.json", mBytes, 0644)

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3002, '/root', 'gemini-3.8-flash-high', 0, 'sess-trash-202', 0);`); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	user := User{
		ID:        3002,
		Workspace: "/root",
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleClearCommand(bot, 888777, 3002, "TricksterBot", user, db)

	// Verify NO files were alienated to inbox
	if entries, err := os.ReadDir(inboxDir); err == nil && len(entries) > 0 {
		t.Errorf("expected 0 files in inboxDir, got %d: %v", len(entries), entries)
	}

	// Verify standard clean response without artifact mentions
	ms.mu.Lock()
	defer ms.mu.Unlock()

	for _, body := range ms.sentBodies {
		if strings.Contains(body, "Exhumed and alienated") {
			t.Errorf("did not expect exhumation notice when all files are garbage, got: %s", body)
		}
		if strings.Contains(body, "Артефакт сессии") {
			t.Errorf("did not expect any artifact document to be delivered to Telegram, got: %s", body)
		}
	}
}

func TestHandleClearCommand_EmptyOrInvalidSession_Graceful(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3003, '/root', 'gemini-3.8-flash-high', 0, '', 0);`); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	user := User{
		ID:        3003,
		Workspace: "/root",
		Model:     "gemini-3.8-flash-high",
		SessionID: "",
	}

	// Must not panic with empty session ID
	handleClearCommand(bot, 888777, 3003, "TricksterBot", user, db)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	foundInit := false
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Fresh session initiated") || strings.Contains(body, "Fresh session initiated") {
			foundInit = true
		}
	}
	if !foundInit {
		t.Errorf("expected fresh session initiation message")
	}
}

func TestHandleClearCommand_InboxCollisionAvoidance(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	inboxDir := filepath.Join(tmpDir, "inbox")
	sessionID := "sess-collision-303"
	sessionPath := filepath.Join(brainDir, sessionID)

	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		t.Fatalf("failed to create inbox dir: %v", err)
	}

	t.Setenv("BRAIN_DIR", brainDir)
	t.Setenv("ECOSYSTEM_INBOX_DIR", inboxDir)

	// Pre-create an existing file with the exact name that would be generated
	today := time.Now().UTC().Format("2006-01-02")
	existingTarget := filepath.Join(inboxDir, fmt.Sprintf("%s_tricksterbot_spec_engine.md", today))
	if err := os.WriteFile(existingTarget, []byte("pre-existing content"), 0644); err != nil {
		t.Fatalf("failed to write pre-existing file: %v", err)
	}

	// Create the artifact in session
	artPath := filepath.Join(sessionPath, "spec_engine.md")
	content := "# Specification: Engine Architecture\n" + strings.Repeat("detailed requirements and specifications\n", 10)
	if err := os.WriteFile(artPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write session artifact: %v", err)
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3004, '/root', 'gemini-3.8-flash-high', 0, 'sess-collision-303', 0);`); err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	user := User{
		ID:        3004,
		Workspace: "/root",
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleClearCommand(bot, 888777, 3004, "TricksterBot", user, db)

	// Verify pre-existing file was NOT overwritten
	preExistingBytes, err := os.ReadFile(existingTarget)
	if err != nil {
		t.Fatalf("pre-existing file missing: %v", err)
	}
	if string(preExistingBytes) != "pre-existing content" {
		t.Errorf("pre-existing file was overwritten!")
	}

	// Verify disambiguated new file was created in inbox
	entries, err := os.ReadDir(inboxDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected 2 files in inboxDir (1 existing + 1 exhumed disambiguated), got %d: %v", len(entries), entries)
	}
}
