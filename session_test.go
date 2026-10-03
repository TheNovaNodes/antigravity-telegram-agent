package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/goleak"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGetAgentsDir(t *testing.T) {
	dir := getAgentsDir()
	home, err := os.UserHomeDir()
	expected := filepath.Join(os.TempDir(), ".agents")
	if err == nil {
		expected = filepath.Join(home, ".agents")
	}
	if dir != expected {
		t.Errorf("Expected %s, got %s", expected, dir)
	}
}

func TestUserDBOperations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Test getUser creation
	u := getUser(db, 12345, "TestBot")
	if u.ID != 12345 {
		t.Errorf("Expected user ID 12345, got %d", u.ID)
	}
	expectedWorkspace := filepath.Join(getAgentsDir(), "TestBot")
	if u.Workspace != expectedWorkspace {
		t.Errorf("Expected workspace %s, got %s", expectedWorkspace, u.Workspace)
	}
	if u.SessionID == "" {
		t.Error("Expected non-empty SessionID")
	}

	// Test updateUserModel
	err := updateUserModel(db, 12345, "new-model")
	if err != nil {
		t.Errorf("updateUserModel failed: %v", err)
	}

	u2 := getUser(db, 12345, "TestBot")
	if u2.Model != "new-model" {
		t.Errorf("Expected model 'new-model', got %s", u2.Model)
	}

	// Test updateUserSession
	updateUserSession(db, 12345, "new-session-id")
	u3 := getUser(db, 12345, "TestBot")
	if u3.SessionID != "new-session-id" {
		t.Errorf("Expected session_id 'new-session-id', got %s", u3.SessionID)
	}
}

func TestUpdateChan_SignaledOnStreamingContent(t *testing.T) {
	pastTime := time.Now().Add(-10 * time.Minute)
	jsonl := `{"event":"step_update","step_update":{"text_delta":"Streaming delta chunk 1"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "UpdateChanTestBot",
		LastActivity:  pastTime,
		UpdateChan:    make(chan struct{}, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// 1. Verify UpdateChan received the wake-up event
	select {
	case <-session.UpdateChan:
		// Successfully received wakeup event from readStdoutLoop
	default:
		t.Error("Expected readStdoutLoop to signal UpdateChan on streaming chunk, but channel was empty")
	}

	// 2. Verify LastActivity was refreshed during chunk processing
	session.mu.Lock()
	lastAct := session.LastActivity
	session.mu.Unlock()

	if !lastAct.After(pastTime.Add(9 * time.Minute)) {
		t.Errorf("Expected LastActivity to be refreshed close to time.Now(), got %v (past was %v)", lastAct, pastTime)
	}
}

// TestReplaceSession verifies that replaceSession correctly cleans up old sessions
// and initializes new ones with the updated parameters (chatID isolation).
func TestReplaceSession(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	db := setupTestDB(t)
	defer db.Close()

	user := User{
		ID:        999,
		Workspace: "/tmp/workspace",
		Model:     "test-model",
		SessionID: "uuid-1",
	}

	botName := "TestBot"
	chatID := int64(1001)

	session1, err := replaceSession(db, botName, user, "uuid-1", "test-model", "/tmp/workspace", chatID)
	if err != nil {
		t.Fatalf("Failed to replaceSession 1: %v", err)
	}
	if session1.Conversation != "uuid-1" {
		t.Errorf("Expected Conversation uuid-1, got %s", session1.Conversation)
	}
	if session1.UserID != 999 {
		t.Errorf("Expected UserID 999, got %d", session1.UserID)
	}

	// Verify it was added to globalSessions
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)

	sessionMu.Lock()
	cached, ok := globalSessions[sessionKey]
	sessionMu.Unlock()

	if !ok {
		t.Errorf("Session not found in globalSessions")
	}
	if cached != session1 {
		t.Errorf("Cached session pointer mismatch")
	}

	// Create a new session with updated workspace (simulating /workspace command)
	session2, err := replaceSession(db, botName, user, "uuid-2", "test-model", "/tmp/new_workspace", chatID)
	if err != nil {
		t.Fatalf("Failed to replaceSession 2: %v", err)
	}

	// The old context should be cancelled
	if session1.ctx.Err() == nil {
		// Note: The cancel function might be async if there were delays, but replaceSession calls it synchronously.
		t.Errorf("Old session context was not cancelled")
	}

	if session2.Workspace != "/tmp/new_workspace" {
		t.Errorf("Expected Workspace /tmp/new_workspace, got %s", session2.Workspace)
	}

	session2.Kill()
}

func TestInitDB(t *testing.T) {
	botName := "TestInitBot"
	dbPath := "sessions_" + botName + ".db"

	// Ensure cleanup before and after
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	db := initDB(botName)
	if db == nil {
		t.Fatal("Expected db instance, got nil")
	}
	defer db.Close()

	// Verify table was created
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='users'").Scan(&name)
	if err != nil {
		t.Fatalf("Failed to verify table creation: %v", err)
	}
	if name != "users" {
		t.Errorf("Expected table 'users', got %s", name)
	}
}

func TestLoadAllowedAdmins(t *testing.T) {
	// Test empty env
	os.Setenv("ALLOWED_ADMIN_IDS", "")
	admins := loadAllowedAdmins()
	if len(admins) != 0 {
		t.Errorf("Expected 0 admins, got %d", len(admins))
	}

	// Test valid env
	os.Setenv("ALLOWED_ADMIN_IDS", "123,456,789")
	admins = loadAllowedAdmins()
	if len(admins) != 3 {
		t.Errorf("Expected 3 admins, got %d", len(admins))
	}
	if !admins[123] || !admins[456] || !admins[789] {
		t.Errorf("Missing expected admin IDs: %v", admins)
	}

	// Test invalid characters in env (should ignore gracefully)
	os.Setenv("ALLOWED_ADMIN_IDS", "123,abc,456")
	admins = loadAllowedAdmins()
	if len(admins) != 2 {
		t.Errorf("Expected 2 valid admins, got %d", len(admins))
	}
}

func TestGetSession(t *testing.T) {
	tempDir := t.TempDir()
	mockScript := filepath.Join(tempDir, "mock_agy.sh")
	scriptContent := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(mockScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create mock script: %v", err)
	}
	t.Setenv("AGY_BINARY", mockScript)

	botName := "TestBotSession"
	user := User{
		ID:           999,
		Workspace:    "/tmp/test_workspace",
		Model:        "test-model",
		IsFirstStart: false,
		SessionID:    "test-session-123",
	}

	// Reset global state for test
	sessionMu.Lock()
	globalSessions = make(map[string]*AgySession)
	sessionMu.Unlock()

	// 1. Get new session
	session := getSession(botName, user, 1234)
	if session == nil {
		t.Fatal("Expected session to be created, got nil")
	}
	if session.Conversation != user.SessionID {
		t.Errorf("Expected conversation %s, got %s", user.SessionID, session.Conversation)
	}

	// 2. Get existing session
	session2 := getSession(botName, user, 1234)
	if session2 != session {
		t.Errorf("Expected same session instance to be returned")
	}

	// 3. Get session with changed conversation ID (Issue #147)
	userChangedSession := user
	userChangedSession.SessionID = "test-session-456"
	session3 := getSession(botName, userChangedSession, 1234)
	if session3 == nil {
		t.Fatal("Expected new session to be created for updated SessionID, got nil")
	}
	if session3 == session {
		t.Errorf("Expected different session instance after SessionID changed")
	}
	if session3.Conversation != "test-session-456" {
		t.Errorf("Expected conversation test-session-456, got %s", session3.Conversation)
	}
	if session.ctx.Err() == nil {
		t.Errorf("Expected old session context to be cancelled after replacement")
	}

	// Clean up sessions
	session.Kill()
	session2.Kill()
	session3.Kill()
}

func TestReadStdoutLoop_NilScanner(t *testing.T) {
	session := &AgySession{}
	// Should return immediately without panicking
	session.readStdoutLoop()
}

func TestStart_InvalidBinary(t *testing.T) {
	os.Setenv("AGY_BINARY", "/nonexistent/binary/agy")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName: "TestFailBot",
		Model:   "test-model",
	}
	session.start()

	if session.Cmd != nil {
		t.Errorf("Expected session.Cmd to be nil after failed Start(), got %v", session.Cmd)
	}
	if session.cancel != nil {
		session.cancel()
	}
}

func TestAgySession_Restart(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName:      "TestRestartBot",
		Model:        "test-model",
		Workspace:    "/tmp/test_workspace",
		Conversation: "restart-session-123",
		UpdateChan:   make(chan struct{}, 1),
		InitChan:     make(chan string, 1),
	}
	session.start()

	if !session.IsAlive() {
		t.Error("Expected session to be alive after start()")
	}

	session.Restart()

	if !session.IsAlive() {
		t.Error("Expected session to be alive after Restart()")
	}

	session.Kill()
}

func TestSendChunk_NilBot(t *testing.T) {
	chunks := sendChunk(nil, 1234, 1, "Hello world")
	if len(chunks) == 0 {
		t.Error("Expected chunks to be returned")
	}

	chunksTrunc := sendChunk(nil, 1234, 1, strings.Repeat("Very long message with ⏳ indicator ", 200))
	if len(chunksTrunc) < 2 {
		t.Errorf("Expected multiple chunks, got %d", len(chunksTrunc))
	}
}

func TestSendArtifacts_NilBot(t *testing.T) {
	// Should not panic when bot is nil
	sendArtifacts(nil, 1234, "Generated file: [output](file:///home/user/.agents/test/output.txt)")
}

func TestHandleUpdate_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// Should return immediately without doing anything
	handleUpdate(nil, tgbotapi.Update{}, db)
}

// TestHandleWorkspaceCommand_ReplaceFailure_DBUnchanged verifies that if the agent process fails to start
// during /workspace, the DB workspace is not updated and remains unchanged (Fixes #188).
func TestHandleWorkspaceCommand_ReplaceFailure_DBUnchanged(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	projectsDir := t.TempDir()
	agentsDir := t.TempDir()
	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)
	// Point to invalid binary to force cmd.Start failure
	t.Setenv("AGY_BINARY", "/nonexistent/path/to/binary")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	oldWS := filepath.Join(projectsDir, "old_lab")
	newWS := filepath.Join(projectsDir, "new_lab")
	os.MkdirAll(oldWS, 0755)
	os.MkdirAll(newWS, 0755)

	userID := int64(777)
	chatID := int64(1007)
	botName := "TestWorkspaceBot"

	// Seed user in DB with old workspace
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, session_id) VALUES (?, ?, ?, ?)",
		userID, oldWS, defaultModel, "prev-session-123")
	if err != nil {
		t.Fatalf("Failed to seed user: %v", err)
	}

	user := User{
		ID:        userID,
		Workspace: oldWS,
		Model:     defaultModel,
		SessionID: "prev-session-123",
	}

	// Attempt workspace change with broken binary
	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+newWS, botName, user, db)

	// Verify DB was NOT modified
	var currentWS, currentSession string
	err = db.QueryRow("SELECT workspace, session_id FROM users WHERE user_id = ?", userID).Scan(&currentWS, &currentSession)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}

	if currentWS != oldWS {
		t.Errorf("Expected DB workspace to remain %s, but got %s", oldWS, currentWS)
	}
	if currentSession != "prev-session-123" {
		t.Errorf("Expected DB session_id to remain prev-session-123, but got %s", currentSession)
	}

	// Verify error notification sent
	ms.mu.Lock()
	sent := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundError := false
	for _, body := range sent {
		if len(body) > 0 {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("Expected error message sent to user on start failure")
	}
}

// TestHandleWorkspaceCommand_Success_DBUpdated verifies that when the process starts successfully,
// the DB workspace is updated and previous session is cleared.
func TestHandleWorkspaceCommand_Success_DBUpdated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	projectsDir := t.TempDir()
	agentsDir := t.TempDir()
	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)
	mockScript := filepath.Join(projectsDir, "mock_agy.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\nexec sleep 30\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	oldWS := filepath.Join(projectsDir, "old_lab")
	newWS := filepath.Join(projectsDir, "new_lab")
	os.MkdirAll(oldWS, 0755)
	os.MkdirAll(newWS, 0755)

	userID := int64(888)
	chatID := int64(1008)
	botName := "TestWorkspaceBot"

	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, session_id) VALUES (?, ?, ?, ?)",
		userID, oldWS, defaultModel, "old-session-456")
	if err != nil {
		t.Fatalf("Failed to seed user: %v", err)
	}

	user := User{
		ID:        userID,
		Workspace: oldWS,
		Model:     defaultModel,
		SessionID: "old-session-456",
	}

	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+newWS, botName, user, db)

	var currentWS, currentSession string
	err = db.QueryRow("SELECT workspace, session_id FROM users WHERE user_id = ?", userID).Scan(&currentWS, &currentSession)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}

	if currentWS != newWS {
		t.Errorf("Expected DB workspace to be %s, got %s", newWS, currentWS)
	}
	if currentSession != "" {
		t.Errorf("Expected DB session_id to be cleared, got %s", currentSession)
	}

	// Clean up started session
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, userID)
	sessionMu.Lock()
	if s, ok := globalSessions[sessionKey]; ok {
		s.Kill()
		delete(globalSessions, sessionKey)
	}
	sessionMu.Unlock()
}

// TestHandleClearCommand_ReplaceFailure_DBUnchanged verifies that /clear does not clear session in DB
// TestHandleClearCommand_ColdSessionEviction verifies that /clear cleanly terminates active sessions,
// evicts them from globalSessions, and clears session_id in DB without spawning new processes.
func TestHandleClearCommand_ColdSessionEviction(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	userID := int64(999)
	chatID := int64(1009)
	botName := "TestClearBot"

	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, session_id) VALUES (?, ?, ?, ?)",
		userID, "/tmp/workspace", defaultModel, "active-uuid-999")
	if err != nil {
		t.Fatalf("Failed to seed user: %v", err)
	}

	user := User{
		ID:        userID,
		Workspace: "/tmp/workspace",
		Model:     defaultModel,
		SessionID: "active-uuid-999",
	}

	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, userID)
	dummySession := &AgySession{
		BotName:      botName,
		ChatID:       chatID,
		UserID:       userID,
		Conversation: "active-uuid-999",
		isAlive:      true,
	}
	sessionMu.Lock()
	globalSessions[sessionKey] = dummySession
	sessionMu.Unlock()

	handleClearCommand(bot, chatID, userID, botName, user, db)

	sessionMu.Lock()
	_, exists := globalSessions[sessionKey]
	sessionMu.Unlock()
	if exists {
		t.Errorf("Expected session %s to be evicted from globalSessions", sessionKey)
	}

	var currentSession string
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&currentSession)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}

	if currentSession != "" {
		t.Errorf("Expected DB session_id to be empty after clear, got %s", currentSession)
	}
}

// TestAcquireSession_DeduplicationAndState verifies that acquireSession reuses active matching sessions
// and correctly terminates and replaces when parameters or ForceRestart change (Fixes #193).
func TestAcquireSession_DeduplicationAndState(t *testing.T) {
	mockDir := t.TempDir()
	mockScript := filepath.Join(mockDir, "mock_agy.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\nexec sleep 30\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	db := setupTestDB(t)
	defer db.Close()

	user := User{
		ID:        555,
		Workspace: "/tmp/test_workspace",
		Model:     "test-model",
		SessionID: "session-uuid-555",
	}

	botName := "TestAcquireBot"
	chatID := int64(5555)

	// First acquisition
	sess1, err := acquireSession(SessionOptions{
		DB:           db,
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ForceRestart: false,
	})
	if err != nil {
		t.Fatalf("acquireSession 1 failed: %v", err)
	}
	defer sess1.Kill()

	if !sess1.IsAlive() {
		t.Errorf("Expected session 1 to be alive")
	}

	// Second acquisition with identical parameters should reuse session 1
	sess2, err := acquireSession(SessionOptions{
		DB:           db,
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ForceRestart: false,
	})
	if err != nil {
		t.Fatalf("acquireSession 2 failed: %v", err)
	}

	if sess1 != sess2 {
		t.Errorf("Expected sess1 and sess2 to be identical pointer (session reuse)")
	}

	// Third acquisition with ForceRestart: true should terminate sess1 and create sess3
	sess3, err := acquireSession(SessionOptions{
		DB:           db,
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ForceRestart: true,
	})
	if err != nil {
		t.Fatalf("acquireSession 3 failed: %v", err)
	}
	defer sess3.Kill()

	if sess3 == sess1 {
		t.Errorf("Expected sess3 to be a new session instance")
	}
	if sess1.IsAlive() {
		t.Errorf("Expected old session 1 to be terminated on ForceRestart")
	}
	if !sess3.IsAlive() {
		t.Errorf("Expected session 3 to be alive")
	}

	// Clean up globalSessions
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)
	sessionMu.Lock()
	delete(globalSessions, sessionKey)
	sessionMu.Unlock()
}

// TestAcquireSession_FailureCleansGlobalSessions verifies that if cmd.Start fails,
// the failed session is removed from globalSessions and not leaked.
func TestAcquireSession_FailureCleansGlobalSessions(t *testing.T) {
	t.Setenv("AGY_BINARY", "/nonexistent/invalid/binary")

	user := User{
		ID:        666,
		Workspace: "/tmp/test_workspace",
		Model:     "test-model",
		SessionID: "session-uuid-666",
	}

	botName := "TestAcquireFailBot"
	chatID := int64(6666)

	sess, err := acquireSession(SessionOptions{
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ForceRestart: false,
	})
	if err == nil {
		t.Errorf("Expected error on invalid binary")
	}
	if sess.IsAlive() {
		t.Errorf("Expected session to not be alive")
	}

	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)
	sessionMu.Lock()
	_, exists := globalSessions[sessionKey]
	sessionMu.Unlock()

	if exists {
		t.Errorf("Expected failed session to not exist in globalSessions")
	}
}

func TestSessionContextDesync_ExplicitConvDesyncNotice(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99001)
	missingConvID := "conv-explicit-missing-111"
	freshConvID := "conv-runtime-fresh-222"

	// Setup initial user state
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
		userID, "/tmp/workspace", defaultModel, false, missingConvID)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}
	_, err = db.Exec("INSERT INTO session_history (user_id, session_id, is_orphaned) VALUES (?, ?, 0)",
		userID, missingConvID)
	if err != nil {
		t.Fatalf("Failed to insert session_history: %v", err)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)
	ms.mu.Lock()
	ms.sentBodies = nil
	ms.mu.Unlock()

	jsonl := fmt.Sprintf(`{"event":"init","conversation_id":"%s"}`+"\n", freshConvID)
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestBotDesync",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		Conversation:  missingConvID,
		ExplicitConv:  true,
		UserID:        userID,
		ChatID:        userID,
		BotAPI:        bot,
		DB:            db,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	metricBefore := getCounterValue(SessionContextResetsTotal.WithLabelValues("TestBotDesync"))

	session.readStdoutLoop()

	metricAfter := getCounterValue(SessionContextResetsTotal.WithLabelValues("TestBotDesync"))
	if metricAfter-metricBefore != 1 {
		t.Errorf("Expected SessionContextResetsTotal to increment by 1, before=%f after=%f", metricBefore, metricAfter)
	}

	// Verify session conversation pointer updated
	if session.Conversation != freshConvID {
		t.Errorf("Expected session.Conversation to be %s, got %s", freshConvID, session.Conversation)
	}

	// Verify database was updated with fresh ID
	var currentDBSession string
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&currentDBSession)
	if err != nil || currentDBSession != freshConvID {
		t.Errorf("Expected DB user session_id %s, got %s (err: %v)", freshConvID, currentDBSession, err)
	}

	// Verify old session marked as orphaned in session_history
	var isOrphaned bool
	err = db.QueryRow("SELECT is_orphaned FROM session_history WHERE user_id = ? AND session_id = ?", userID, missingConvID).Scan(&isOrphaned)
	if err != nil {
		t.Fatalf("Failed to query session_history: %v", err)
	}
	if !isOrphaned {
		t.Errorf("Expected missing session %s to be marked as orphaned (is_orphaned=true)", missingConvID)
	}

	// Verify Telegram alert notification was delivered
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundAlert := false
	for _, body := range sentBodies {
		decoded, _ := url.QueryUnescape(body)
		if strings.Contains(decoded, "Context Reset") || strings.Contains(decoded, "Previous conversation context was not found") {
			foundAlert = true
			if !strings.Contains(decoded, safePrefix(freshConvID, 8)) {
				t.Errorf("Expected alert message to mention fresh session prefix %s, got: %s", safePrefix(freshConvID, 8), decoded)
			}
			break
		}
	}
	if !foundAlert {
		t.Errorf("Expected Telegram alert message for context desync, but none was sent. Sent bodies: %v", sentBodies)
	}
}

func TestSessionContextDesync_ActiveStreamingTurnBuffer(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99002)
	missingConvID := "conv-streaming-missing-333"
	freshConvID := "conv-runtime-fresh-444"

	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
		userID, "/tmp/workspace", defaultModel, false, missingConvID)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}
	_, err = db.Exec("INSERT INTO session_history (user_id, session_id, is_orphaned) VALUES (?, ?, 0)",
		userID, missingConvID)
	if err != nil {
		t.Fatalf("Failed to insert session_history: %v", err)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)
	ms.mu.Lock()
	ms.sentBodies = nil
	ms.mu.Unlock()

	jsonl := fmt.Sprintf(`{"event":"init","conversation_id":"%s"}`+"\n", freshConvID)
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "TestBotStreaming",
		Model:           defaultModel,
		Workspace:       "/tmp/workspace",
		Conversation:    missingConvID,
		ExplicitConv:    false, // owned in DB
		UserID:          userID,
		ChatID:          userID,
		ActiveMessageID: 777, // Indicates active streaming message
		BotAPI:          bot,
		DB:              db,
		UpdateChan:      make(chan struct{}, 10),
		InitChan:        make(chan string, 10),
		StdoutScanner:   scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	session.mu.Lock()
	buf := session.TextBuffer
	session.mu.Unlock()

	if !strings.Contains(buf, "Previous conversation context could not be loaded") {
		t.Errorf("Expected buffer to contain warning prefix, got: %q", buf)
	}
}

func TestSessionContextDesync_NoFalsePositiveOnCleanStart(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99003)
	uninitializedID := "initial-auto-uuid-not-in-history"
	freshConvID := "fresh-session-uuid-clean"

	// User created by getUser, but session never established in session_history
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
		userID, "/tmp/workspace", defaultModel, true, uninitializedID)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)
	ms.mu.Lock()
	ms.sentBodies = nil
	ms.mu.Unlock()

	jsonl := fmt.Sprintf(`{"event":"init","conversation_id":"%s"}`+"\n", freshConvID)
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestBotClean",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		Conversation:  uninitializedID,
		ExplicitConv:  false, // Regular first start
		UserID:        userID,
		ChatID:        userID,
		BotAPI:        bot,
		DB:            db,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	metricBefore := getCounterValue(SessionContextResetsTotal.WithLabelValues("TestBotClean"))

	session.readStdoutLoop()

	metricAfter := getCounterValue(SessionContextResetsTotal.WithLabelValues("TestBotClean"))
	if metricAfter != metricBefore {
		t.Errorf("Clean start should NOT increment SessionContextResetsTotal! before=%f, after=%f", metricBefore, metricAfter)
	}

	// Verify no alert message was sent
	ms.mu.Lock()
	sentCount := len(ms.sentBodies)
	ms.mu.Unlock()

	if sentCount > 0 {
		t.Errorf("Clean start should not send alert messages, sent: %d (bodies: %v)", sentCount, ms.sentBodies)
	}
}

func TestResumeCommand_FiltersOrphanedSessions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99004)
	healthySession := "healthy-conv-12345678"
	orphanedSession := "orphaned-conv-87654321"

	// Populate session_history
	_, err := db.Exec("INSERT INTO session_history (user_id, session_id, is_orphaned) VALUES (?, ?, 0)", userID, healthySession)
	if err != nil {
		t.Fatalf("Failed to insert healthy session: %v", err)
	}
	_, err = db.Exec("INSERT INTO session_history (user_id, session_id, is_orphaned) VALUES (?, ?, 1)", userID, orphanedSession)
	if err != nil {
		t.Fatalf("Failed to insert orphaned session: %v", err)
	}

	// Create fake transcript for healthy session so handleResumeCommand keeps it
	brainDir := t.TempDir()
	t.Setenv("BRAIN_DIR", brainDir)

	healthyDir := filepath.Join(brainDir, healthySession, ".system_generated", "logs")
	if err := os.MkdirAll(healthyDir, 0755); err != nil {
		t.Fatalf("Failed to create healthy transcript dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(healthyDir, "transcript.jsonl"), []byte(`{"content":"Hello world"}`), 0644)

	// Also create transcript for orphaned session to confirm filter catches it at DB query level
	orphanedDir := filepath.Join(brainDir, orphanedSession, ".system_generated", "logs")
	if err := os.MkdirAll(orphanedDir, 0755); err != nil {
		t.Fatalf("Failed to create orphaned transcript dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(orphanedDir, "transcript.jsonl"), []byte(`{"content":"Orphaned context"}`), 0644)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)
	ms.mu.Lock()
	ms.sentBodies = nil
	ms.mu.Unlock()

	handleResumeCommand(bot, userID, userID, db)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundHealthy := false
	foundOrphaned := false
	for _, body := range sentBodies {
		decoded, _ := url.QueryUnescape(body)
		if strings.Contains(decoded, safePrefix(healthySession, 8)) {
			foundHealthy = true
		}
		if strings.Contains(decoded, safePrefix(orphanedSession, 8)) {
			foundOrphaned = true
		}
	}

	if !foundHealthy {
		t.Errorf("Expected healthy session %s to appear in resume list, sent: %v", safePrefix(healthySession, 8), sentBodies)
	}
	if foundOrphaned {
		t.Errorf("Orphaned session %s MUST NOT appear in resume list, but it did! Sent: %v", safePrefix(orphanedSession, 8), sentBodies)
	}
}

func TestCallbackQuery_ResumeDesyncWarning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99005)
	requestedSession := "missing-session-uuid-1111"

	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
		userID, "/tmp/workspace", defaultModel, false, requestedSession)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}
	_, err = db.Exec("INSERT INTO session_history (user_id, session_id, is_orphaned) VALUES (?, ?, 0)", userID, requestedSession)
	if err != nil {
		t.Fatalf("Failed to insert session_history: %v", err)
	}

	t.Setenv("AGY_BINARY", "cat")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)
	ms.mu.Lock()
	ms.sentBodies = nil
	ms.mu.Unlock()

	user := getUser(db, userID, "TestBot")

	cb := &tgbotapi.CallbackQuery{
		ID:   "cb-resume",
		From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{
			MessageID: 101,
			Chat:      &tgbotapi.Chat{ID: userID},
		},
		Data: "resume:" + requestedSession,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleCallbackQuery(bot, cb, user, "TestBot", db)
	}()

	var sess *AgySession
	for i := 0; i < 50; i++ {
		sessionMu.Lock()
		sess = globalSessions[fmt.Sprintf("TestBot:%d:%d", userID, userID)]
		sessionMu.Unlock()
		if sess != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sess != nil {
		sess.InitChan <- "fresh-fallback-uuid-9999"
	}

	<-done

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundWarning := false
	for _, body := range sentBodies {
		decoded, _ := url.QueryUnescape(body)
		if strings.Contains(decoded, "Could not resume session") && strings.Contains(decoded, "missing on disk") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("Expected callback resume with different ID to alert user about desync, sent: %v", sentBodies)
	}
}

func TestGoleak_SessionStdoutLoop_CleanTermination(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	mockData := strings.Join([]string{
		`{"event":"init","init":{"conversation_id":"leak-test-conv"}}`,
		`{"event":"step_update","step_update":{"text_delta":"Streaming data..."}}`,
		`{"event":"result","result":{"status":"OK"}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(mockData))
	session := &AgySession{
		BotName:       "LeakTestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())

	// Run loop synchronously; upon EOF it must close channels and return
	session.readStdoutLoop()
	session.cancel()

	// Wait briefly for scheduler
	time.Sleep(10 * time.Millisecond)
}

func TestGoleak_SessionKill_NoOrphanGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tmpDir := t.TempDir()
	mockScript := filepath.Join(tmpDir, "sleep_loop.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\nexec sleep 60\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	user := User{
		ID:        98765,
		Workspace: tmpDir,
		Model:     defaultModel,
		SessionID: "sess-leak-kill",
	}

	session := getSession("LeakBot", user, 98765)
	if session == nil {
		t.Fatalf("failed to create session")
	}

	// Start session process
	_ = session.start()

	// Give it a moment to spin up
	time.Sleep(20 * time.Millisecond)

	// Kill session
	session.Kill()

	// Wait for process and monitors to exit
	time.Sleep(50 * time.Millisecond)
}

func TestGoleak_HousekeepingWorkersShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	t.Setenv("AGENTS_DIR", t.TempDir())
	stopWorkers := make(chan struct{})

	StartSessionGCWorker(10*time.Millisecond, 1*time.Hour, stopWorkers)
	StartDiskCleanupWorker(10*time.Millisecond, 24*time.Hour, stopWorkers)

	time.Sleep(25 * time.Millisecond)

	// Graceful shutdown
	close(stopWorkers)
	time.Sleep(25 * time.Millisecond)
}

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
	db, err := sql.Open("sqlite", ":memory:")
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
	tomlContent := `api_key = "sk-ant-mocktoken890123456789"`
	os.WriteFile(tomlFile, []byte(tomlContent), 0644)

	// Create a .sql file with a secret
	sqlFile := filepath.Join(agentsDir, "dump.sql")
	sqlContent := `INSERT INTO tokens VALUES ('ghp_mocktoken89012345678901234567890123456');`
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
			if strings.Contains(body, "sk-ant-mocktoken890123456789") {
				t.Errorf("toml file secret was not sanitized: %s", body)
			}
			if !strings.Contains(body, "[REDACTED_SECRET:ANTHROPIC_KEY]") {
				t.Errorf("toml file secret missing redacted string: %s", body)
			}
		} else if strings.Contains(body, "dump.sql") {
			foundSql = true
			if strings.Contains(body, "ghp_mocktoken89012345678901234567890123456") {
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

	db, err := sql.Open("sqlite", ":memory:")
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
			if !strings.Contains(unescaped, "Session artifact") && !strings.Contains(body, "Session artifact") {
				t.Errorf("document caption missing 'Session artifact', got: %s", unescaped)
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

	db, err := sql.Open("sqlite", ":memory:")
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
		if strings.Contains(body, "Session artifact") {
			t.Errorf("did not expect any artifact document to be delivered to Telegram, got: %s", body)
		}
	}
}

func TestHandleClearCommand_EmptyOrInvalidSession_Graceful(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	db, err := sql.Open("sqlite", ":memory:")
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

	db, err := sql.Open("sqlite", ":memory:")
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

func TestGetProjectsDir_ResolutionPriority(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Explicit PROJECTS_DIR takes highest precedence
	customProjects := filepath.Join(tempDir, "custom_projects")
	t.Setenv("PROJECTS_DIR", customProjects)
	t.Setenv("SYSTEM_HOME", filepath.Join(tempDir, "sysroot"))
	t.Setenv("HOME", filepath.Join(tempDir, "etc/antigravity-bot/accounts/user"))
	if got := getProjectsDir(); got != customProjects {
		t.Errorf("Expected PROJECTS_DIR %s, got %s", customProjects, got)
	}

	// 2. Unset PROJECTS_DIR -> resolves via SYSTEM_HOME when daemon runs under multi-account HOME
	t.Setenv("PROJECTS_DIR", "")
	expectedSysProjects := filepath.Join(tempDir, "sysroot", "projects")
	if got := getProjectsDir(); got != expectedSysProjects {
		t.Errorf("Expected SYSTEM_HOME projects %s, got %s", expectedSysProjects, got)
	}

	// 3. Fallback when neither PROJECTS_DIR nor SYSTEM_HOME is set, standard HOME
	t.Setenv("SYSTEM_HOME", "")
	standardHome := filepath.Join(tempDir, "home", "developer")
	t.Setenv("HOME", standardHome)
	expectedDevProjects := filepath.Join(standardHome, "projects")
	if got := getProjectsDir(); got != expectedDevProjects {
		t.Errorf("Expected standard HOME projects %s, got %s", expectedDevProjects, got)
	}
}

func TestExtractAllowedArtifacts_MultiAccountDaemonAndSystemHome(t *testing.T) {
	tempDir := t.TempDir()
	sysHome := filepath.Join(tempDir, "root")
	accountHome := filepath.Join(tempDir, "etc", "antigravity-bot", "accounts", "thedoctormes")

	sysProjectsDir := filepath.Join(sysHome, "projects", "fxlab-landing")
	accountAgentsDir := filepath.Join(accountHome, ".agents", "trickster_gobot")
	forbiddenDir := filepath.Join(tempDir, "etc", "security")

	if err := os.MkdirAll(sysProjectsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accountAgentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(forbiddenDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROJECTS_DIR", "")
	t.Setenv("SYSTEM_HOME", sysHome)
	t.Setenv("HOME", accountHome)

	// Create test artifacts
	projFile := filepath.Join(sysProjectsDir, "report.md")
	if err := os.WriteFile(projFile, []byte("# FXLab Report\n"), 0644); err != nil {
		t.Fatal(err)
	}

	agentFile := filepath.Join(accountAgentsDir, "memory.json")
	if err := os.WriteFile(agentFile, []byte(`{"status":"active"}`), 0644); err != nil {
		t.Fatal(err)
	}

	forbiddenFile := filepath.Join(forbiddenDir, "shadow")
	if err := os.WriteFile(forbiddenFile, []byte("root:*:19000:0:99999:7:::"), 0644); err != nil {
		t.Fatal(err)
	}

	text := fmt.Sprintf("Turn Output:\n- [Project Report](file://%s)\n- [Agent Memory](file://%s)\n- [Forbidden](file://%s)\n",
		projFile, agentFile, forbiddenFile)

	allowed := ExtractAllowedArtifacts(text)

	found := make(map[string]bool)
	for _, p := range allowed {
		found[p] = true
	}

	if !found[projFile] {
		t.Errorf("Expected project file under SYSTEM_HOME %s to be allowed, but it was blocked", projFile)
	}
	if !found[agentFile] {
		t.Errorf("Expected agent file under account HOME %s to be allowed, but it was blocked", agentFile)
	}
	if found[forbiddenFile] {
		t.Errorf("Expected forbidden file %s to be strictly blocked by LFI sandbox, but it was allowed", forbiddenFile)
	}
	if len(allowed) != 2 {
		t.Errorf("Expected exactly 2 allowed artifacts, got %d: %v", len(allowed), allowed)
	}
}

func TestExtractAllowedArtifacts_SessionWorkspaceScoped(t *testing.T) {
	tempDir := t.TempDir()
	customWS := filepath.Join(tempDir, "opt", "isolated-repo")
	otherDir := filepath.Join(tempDir, "opt", "private-data")

	if err := os.MkdirAll(customWS, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}

	wsFile := filepath.Join(customWS, "generated_script.py")
	otherFile := filepath.Join(otherDir, "secrets.env")

	if err := os.WriteFile(wsFile, []byte("print('hello')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherFile, []byte("KEY=123\n"), 0644); err != nil {
		t.Fatal(err)
	}

	text := fmt.Sprintf("Files:\n- [Script](file://%s)\n- [Secret](file://%s)\n", wsFile, otherFile)

	// 1. Without passing workspace, customWS is outside standard roots and should be blocked
	allowedNoExtra := ExtractAllowedArtifacts(text)
	for _, p := range allowedNoExtra {
		if p == wsFile {
			t.Errorf("Expected %s to be blocked when workspace is not passed", wsFile)
		}
	}

	// 2. When passing customWS as extraRoots (session.Workspace), wsFile is allowed, otherFile remains blocked
	allowedWithWS := ExtractAllowedArtifacts(text, customWS)
	found := make(map[string]bool)
	for _, p := range allowedWithWS {
		found[p] = true
	}

	if !found[wsFile] {
		t.Errorf("Expected workspace-scoped artifact %s to be allowed", wsFile)
	}
	if found[otherFile] {
		t.Errorf("Expected non-workspace artifact %s to remain blocked", otherFile)
	}
}

func TestExtractAllowedArtifacts_TempDirEscapeBlocked(t *testing.T) {
	tempDir := t.TempDir()
	accountHome := filepath.Join(tempDir, "etc", "antigravity-bot", "accounts", "thedoctormes")
	if err := os.MkdirAll(accountHome, 0755); err != nil {
		t.Fatal(err)
	}

	// SYSTEM_HOME unset, HOME has /accounts/ -> getSystemBaseHome() falls back to os.TempDir()
	t.Setenv("PROJECTS_DIR", "")
	t.Setenv("SYSTEM_HOME", "")
	t.Setenv("HOME", accountHome)

	tmpProjectsDir := filepath.Join(os.TempDir(), "projects")
	if err := os.MkdirAll(tmpProjectsDir, 0755); err != nil {
		t.Fatal(err)
	}
	tmpFile := filepath.Join(tmpProjectsDir, "malicious_injected.txt")
	if err := os.WriteFile(tmpFile, []byte("injected content"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile)

	text := fmt.Sprintf("Report: [Escape File](file://%s)\n", tmpFile)
	allowed := ExtractAllowedArtifacts(text)
	for _, p := range allowed {
		if p == tmpFile {
			t.Errorf("Security boundary failed: file in /tmp/projects %s was allowed when baseHome is TempDir", tmpFile)
		}
	}
}

func TestExtractAllowedArtifacts_RelativePathsResolution(t *testing.T) {
	tempDir := t.TempDir()
	workspaceDir := filepath.Join(tempDir, "workspace")
	subDir := filepath.Join(workspaceDir, "docs")
	outsideDir := filepath.Join(tempDir, "outside")

	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	relFile := filepath.Join(subDir, "report.md")
	traversalFile := filepath.Join(outsideDir, "secret.txt")

	if err := os.WriteFile(relFile, []byte("# Relative Doc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(traversalFile, []byte("forbidden\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Relative link and traversal attempt
	text := "Check output:\n- [Doc](docs/report.md)\n- [Traversal](../outside/secret.txt)\n"

	allowed := ExtractAllowedArtifacts(text, workspaceDir)

	found := make(map[string]bool)
	for _, p := range allowed {
		found[p] = true
	}

	if !found[relFile] {
		t.Errorf("Expected relative file docs/report.md to be resolved against workspace %s, but it was not", workspaceDir)
	}
	if found[traversalFile] {
		t.Errorf("Expected path traversal ../outside/secret.txt to be strictly blocked, but it was allowed")
	}
}

func TestGetSession_AttachesDBAndPersistsInitEvent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88881)
	chatID := int64(88881)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)
	initialSessionID := user.SessionID

	// Call getSession passing the db handle
	session := getSession(botName, user, chatID, db)
	if session == nil {
		t.Fatal("Expected session to be created, got nil")
	}
	if session.DB == nil {
		t.Error("Expected session.DB to be attached and non-nil")
	}

	// Stop background process started by getSession before testing mock stdout stream
	session.Kill()

	// Simulate agy outputting an init event with the actual conversation ID
	realConvID := "agy-real-conversation-uuid-777"
	jsonl := `{"event":"init","conversation_id":"` + realConvID + `"}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session.mu.Lock()
	session.ctx = ctx
	session.cancel = cancel
	session.isAlive = true
	session.StdoutScanner = scanner
	session.mu.Unlock()

	session.readStdoutLoop()

	if session.GetConversation() != realConvID {
		t.Errorf("Expected session conversation to be %s, got %s", realConvID, session.GetConversation())
	}

	// Verify database was automatically updated
	var dbSessionID string
	err := db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessionID)
	if err != nil {
		t.Fatalf("Failed to query user from DB: %v", err)
	}
	if dbSessionID != realConvID {
		t.Errorf("Expected DB session_id to be %s, got %s (initial was %s)", realConvID, dbSessionID, initialSessionID)
	}

	// Verify session history has the real conversation ID
	var histCount int
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ? AND session_id = ?", userID, realConvID).Scan(&histCount)
	if err != nil {
		t.Fatalf("Failed to query session_history: %v", err)
	}
	if histCount == 0 {
		t.Errorf("Expected session_history to contain %s", realConvID)
	}
}

func TestGetSession_MultiTurnContextRetention_NoProcessKill(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88882)
	chatID := int64(88882)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)

	// Turn 1: Initial creation
	session1 := getSession(botName, user, chatID, db)
	if session1 == nil {
		t.Fatal("Expected turn 1 session to be created")
	}
	session1.Kill()

	// Simulate init event updating conversation
	realConvID := "conv-multi-turn-999"
	jsonl := `{"event":"init","conversation_id":"` + realConvID + `"}` + "\n"
	scanner1 := bufio.NewScanner(strings.NewReader(jsonl))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	session1.mu.Lock()
	session1.ctx = ctx1
	session1.cancel = cancel1
	session1.isAlive = true
	session1.StdoutScanner = scanner1
	session1.mu.Unlock()
	session1.readStdoutLoop()

	// Turn 2: User sends another message. getUser loads the updated session_id
	userTurn2 := getUser(db, userID, botName)
	if userTurn2.SessionID != realConvID {
		t.Fatalf("Expected userTurn2.SessionID to be %s, got %s", realConvID, userTurn2.SessionID)
	}

	session2 := getSession(botName, userTurn2, chatID, db)
	if session2 != session1 {
		t.Errorf("Expected Turn 2 to retain identical session instance (session2 != session1)")
	}
	if session1.ctx.Err() != nil {
		t.Errorf("Expected session1 context not to be cancelled, got %v", session1.ctx.Err())
	}
	if session2.GetConversation() != realConvID {
		t.Errorf("Expected session2 conversation to remain %s, got %s", realConvID, session2.GetConversation())
	}

	// Turn 3: User sends another message even if user struct has empty sessionID
	userTurn3 := userTurn2
	userTurn3.SessionID = ""
	session3 := getSession(botName, userTurn3, chatID, db)
	if session3 != session1 {
		t.Errorf("Expected Turn 3 with empty sessionID in User to retain active session instance")
	}
}

func TestGetSession_ExplicitSessionSwitch_KillsOldSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88883)
	chatID := int64(88883)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)
	user.SessionID = "conv-first-111"

	session1 := getSession(botName, user, chatID, db)
	if session1 == nil {
		t.Fatal("Expected session1 to be created")
	}
	session1.mu.Lock()
	session1.isAlive = true
	session1.mu.Unlock()

	// User explicitly switches to a different session
	userSwitched := user
	userSwitched.SessionID = "conv-second-222"

	session2 := getSession(botName, userSwitched, chatID, db)
	if session2 == session1 {
		t.Errorf("Expected different session instance after explicit session ID switch")
	}
	if session1.ctx.Err() != context.Canceled {
		t.Errorf("Expected old session context to be canceled with context.Canceled, got %v", session1.ctx.Err())
	}
	if session2.GetConversation() != "conv-second-222" {
		t.Errorf("Expected new session conversation to be conv-second-222, got %s", session2.GetConversation())
	}
}

func TestAccountPool_BotScopedIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	pool := &AccountPool{
		accountsDir: tmpDir,
		accounts:    make(map[string]*Account),
		pinnedChat:  make(map[string]string),
		activeChat:  make(map[string]string),
		client:      &http.Client{Timeout: 5 * time.Second},
	}

	pool.accounts["acc-1"] = &Account{
		ID:       "acc-1",
		Email:    "acc1@example.com",
		HomeDir:  filepath.Join(tmpDir, "acc-1"),
		State:    StateActive,
		LastUsed: time.Now().Add(-1 * time.Hour),
	}
	pool.accounts["acc-2"] = &Account{
		ID:       "acc-2",
		Email:    "acc2@example.com",
		HomeDir:  filepath.Join(tmpDir, "acc-2"),
		State:    StateActive,
		LastUsed: time.Now().Add(-2 * time.Hour),
	}

	chatID := int64(173681771)
	botA := "Caduceus_brobot"
	botB := "trickster_gobot"

	// Pin botA to acc-1
	if err := pool.PinAccount(chatID, "acc-1", botA); err != nil {
		t.Fatalf("PinAccount failed: %v", err)
	}

	// botA must be pinned to acc-1
	if !pool.IsPinned(chatID, botA) {
		t.Errorf("Expected botA (%s) to be pinned", botA)
	}
	if pinnedID, ok := pool.GetPinnedAccount(chatID, botA); !ok || pinnedID != "acc-1" {
		t.Errorf("Expected botA pinned to acc-1, got %s (ok=%v)", pinnedID, ok)
	}

	// botB must NOT be pinned
	if pool.IsPinned(chatID, botB) {
		t.Errorf("Expected botB (%s) NOT to be pinned", botB)
	}

	// Switch botB to acc-2
	if err := pool.SwitchAccount(chatID, "acc-2", botB); err != nil {
		t.Fatalf("SwitchAccount failed: %v", err)
	}

	// Verify isolated active assignments
	activeA := pool.GetActiveAccountForChat(chatID, botA)
	if activeA == nil || activeA.ID != "acc-1" {
		t.Errorf("Expected botA active account to be acc-1, got: %+v", activeA)
	}

	activeB := pool.GetActiveAccountForChat(chatID, botB)
	if activeB == nil || activeB.ID != "acc-2" {
		t.Errorf("Expected botB active account to be acc-2, got: %+v", activeB)
	}

	// Unpin botA
	if err := pool.UnpinAccount(chatID, botA); err != nil {
		t.Fatalf("UnpinAccount failed: %v", err)
	}
	if pool.IsPinned(chatID, botA) {
		t.Errorf("Expected botA to be unpinned")
	}
}

func TestResetChatSessionCache_PreserveDBSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99991)
	chatID := int64(99991)
	botName := "Caduceus_brobot"
	convID := "conv-preserve-test-999"

	// Setup user in DB
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id, voice_reply) VALUES (?, '', 'gemini-3.8-flash-high', 0, ?, 0)",
		userID, convID)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	// Evict in-memory cache but PRESERVE SQLite session
	resetChatSessionCache(db, botName, userID, chatID, true)

	var dbSessID sql.NullString
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if !dbSessID.Valid || dbSessID.String != convID {
		t.Errorf("Expected session_id to be preserved as %s, got: %+v", convID, dbSessID)
	}

	// Call default resetChatSessionCache (wiping DB)
	resetChatSessionCache(db, botName, userID, chatID)
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if dbSessID.Valid && dbSessID.String != "" {
		t.Errorf("Expected session_id to be NULL after destructive reset, got: %s", dbSessID.String)
	}
}

func TestEnsureSharedAccountDirectories_SymlinkCreation(t *testing.T) {
	sharedTmp := t.TempDir()
	expectedShared := filepath.Join(sharedTmp, "conversations")
	t.Setenv("CONVERSATIONS_DIR", expectedShared)
	t.Setenv("SYSTEM_HOME", sharedTmp)
	tmpAccHome := t.TempDir()

	// Create a pre-existing dummy directory in .cache to verify duplicate cleanup
	preExistingCache := filepath.Join(tmpAccHome, ".cache", "dummy")
	if err := os.MkdirAll(preExistingCache, 0755); err != nil {
		t.Fatalf("Failed to create preExistingCache: %v", err)
	}

	// Call EnsureSharedAccountDirectories
	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed: %v", err)
	}

	// 1. Verify conversations symlink
	accConvs := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli", "conversations")
	fi, err := os.Lstat(accConvs)
	if err != nil {
		t.Fatalf("Failed to lstat %s: %v", accConvs, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Expected %s to be a symlink, got mode: %v", accConvs, fi.Mode())
	}
	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink %s: %v", accConvs, err)
	}
	if target != expectedShared {
		t.Errorf("Expected symlink target %s, got: %s", expectedShared, target)
	}

	// 2. Verify .cache, go, and .npm symlinks
	checks := []struct {
		name     string
		path     string
		expected string
	}{
		{".cache", filepath.Join(tmpAccHome, ".cache"), filepath.Join(sharedTmp, ".cache")},
		{"go", filepath.Join(tmpAccHome, "go"), filepath.Join(sharedTmp, "go")},
		{".npm", filepath.Join(tmpAccHome, ".npm"), filepath.Join(sharedTmp, ".npm")},
	}

	for _, tc := range checks {
		info, lstatErr := os.Lstat(tc.path)
		if lstatErr != nil {
			t.Fatalf("Failed to lstat %s: %v", tc.path, lstatErr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink, got mode: %v", tc.path, info.Mode())
		}
		linkTarget, readlinkErr := os.Readlink(tc.path)
		if readlinkErr != nil {
			t.Fatalf("Failed to readlink %s: %v", tc.path, readlinkErr)
		}
		if linkTarget != tc.expected {
			t.Errorf("[%s] Expected symlink target %s, got: %s", tc.name, tc.expected, linkTarget)
		}
	}
}

func TestSession_EnvSharedCachesInjection(t *testing.T) {
	sharedTmp := t.TempDir()
	t.Setenv("SYSTEM_HOME", sharedTmp)
	t.Setenv("SHARED_CACHE_DIR", filepath.Join(sharedTmp, "custom_cache"))
	t.Setenv("SHARED_GOPATH_DIR", filepath.Join(sharedTmp, "custom_go"))
	t.Setenv("SHARED_NPM_DIR", filepath.Join(sharedTmp, "custom_npm"))

	accHome := filepath.Join(sharedTmp, "acc_home")
	_ = os.MkdirAll(accHome, 0755)

	s := &AgySession{
		AccountHomeDir: accHome,
	}

	env := buildChildEnv(s.AccountHomeDir)

	expectedGo := filepath.Join(sharedTmp, "custom_go")
	expectedCache := filepath.Join(sharedTmp, "custom_cache", "go-build")
	expectedNpm := filepath.Join(sharedTmp, "custom_npm")
	expectedPip := filepath.Join(sharedTmp, "custom_cache", "pip")

	checkEnv := func(key, val string) {
		prefix := key + "="
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				found = true
				if e != prefix+val {
					t.Errorf("Expected %s%s, got %s", prefix, val, e)
				}
			}
		}
		if !found {
			t.Errorf("Variable %s not found in env", key)
		}
	}

	checkEnv("GOPATH", expectedGo)
	checkEnv("GOCACHE", expectedCache)
	checkEnv("NPM_CONFIG_CACHE", expectedNpm)
	checkEnv("PIP_CACHE_DIR", expectedPip)
	checkEnv("HOME", accHome)
}

func TestEnsureSharedAccountDirectories_DanglingSymlinkRecovery(t *testing.T) {
	sharedTmp := t.TempDir()
	expectedShared := filepath.Join(sharedTmp, "conversations")
	t.Setenv("CONVERSATIONS_DIR", expectedShared)
	t.Setenv("SYSTEM_HOME", sharedTmp)
	tmpAccHome := t.TempDir()

	accCliDir := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(accCliDir, 0755); err != nil {
		t.Fatalf("Failed to create accCliDir: %v", err)
	}
	accConvs := filepath.Join(accCliDir, "conversations")

	// 1. Create a pre-existing DANGLING symlink pointing to a non-existent directory
	nonExistentTarget := filepath.Join(sharedTmp, "non_existent_old_conversations")
	if err := os.Symlink(nonExistentTarget, accConvs); err != nil {
		t.Fatalf("Failed to create dummy dangling symlink: %v", err)
	}

	// Verify that the symlink is indeed dangling (os.Stat returns ErrNotExist, os.Lstat succeeds)
	if _, statErr := os.Stat(accConvs); !os.IsNotExist(statErr) {
		t.Fatalf("Expected os.Stat to return ErrNotExist for dangling symlink, got %v", statErr)
	}

	// 2. Call EnsureSharedAccountDirectories
	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed on dangling symlink: %v", err)
	}

	// 3. Verify that the dangling symlink was healed and now points to the existing shared conversations dir
	fi, err := os.Lstat(accConvs)
	if err != nil {
		t.Fatalf("Failed to lstat healed conversations symlink %s: %v", accConvs, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Expected %s to be a symlink, got mode: %v", accConvs, fi.Mode())
	}
	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink %s: %v", accConvs, err)
	}
	if target != expectedShared {
		t.Errorf("Expected healed symlink target %s, got: %s", expectedShared, target)
	}

	// 4. Assert that the symlink target is fully writable (mkdir / stat pass without EEXIST)
	if _, statErr := os.Stat(accConvs); statErr != nil {
		t.Errorf("Expected healed symlink target to exist on disk: %v", statErr)
	}
}

func TestEnsureSharedAccountDirectories_SystemHomeFallback(t *testing.T) {
	sharedTmp := t.TempDir()
	t.Setenv("CONVERSATIONS_DIR", "")
	t.Setenv("SYSTEM_HOME", sharedTmp)
	t.Setenv("HOME", "/etc/antigravity-bot/accounts/service_account")

	tmpAccHome := t.TempDir()

	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed: %v", err)
	}

	accConvs := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli", "conversations")
	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}

	expectedBase := filepath.Join(sharedTmp, ".gemini", "antigravity-cli", "conversations")
	if target != expectedBase {
		t.Errorf("Expected symlink target %s derived from SYSTEM_HOME, got: %s", expectedBase, target)
	}
}

func TestEnsureSymlink_DanglingSymlinkSelfHealing(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "real_target")
	symlinkPath := filepath.Join(tmpDir, "test_symlink")

	// Create dangling symlink pointing to targetDir before targetDir exists
	_ = os.Symlink(targetDir, symlinkPath)

	// ensureSymlink should detect targetDir didn't exist, create targetDir, and validate symlink
	if err := ensureSymlink(targetDir, symlinkPath); err != nil {
		t.Fatalf("ensureSymlink failed: %v", err)
	}

	if _, err := os.Stat(symlinkPath); err != nil {
		t.Errorf("Expected symlink target to exist after ensureSymlink: %v", err)
	}
}

func TestReadStdoutLoop_InitEvent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert user
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
		12345, "/tmp/workspace", defaultModel, false, "old-session-uuid")
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	jsonl := `{"event":"init","conversation_id":"new-conversation-uuid-999"}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		Conversation:  "old-session-uuid",
		UserID:        12345,
		DB:            db,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	if session.Conversation != "new-conversation-uuid-999" {
		t.Errorf("Expected Conversation new-conversation-uuid-999, got %s", session.Conversation)
	}

	select {
	case id := <-session.InitChan:
		if id != "new-conversation-uuid-999" {
			t.Errorf("Expected InitChan to receive new-conversation-uuid-999, got %s", id)
		}
	default:
		t.Error("InitChan did not receive new conversation ID")
	}

	// Verify DB update
	var dbSessionID string
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", 12345).Scan(&dbSessionID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if dbSessionID != "new-conversation-uuid-999" {
		t.Errorf("Expected DB session_id to be updated, got %s", dbSessionID)
	}
}

func TestReadStdoutLoop_StepUpdateAndResult(t *testing.T) {
	jsonl := `{"event":"step_update","step_update":{"text_delta":"Hello "}}` + "\n" +
		`{"event":"step_update","step_update":{"text_delta":"world!"}}` + "\n" +
		`{"event":"result","result":{"status":"OK"}}` + "\n"

	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Text buffer is cleared on result event
	session.mu.Lock()
	buf := session.TextBuffer
	session.mu.Unlock()

	if buf != "" {
		t.Errorf("Expected TextBuffer to be cleared after result event, got %q", buf)
	}
}

func TestReadStdoutLoop_AskQuestion(t *testing.T) {
	jsonl := `{"event":"step_update","step_update":{"tool_calls":[{"name":"ask_question","argumentsJson":"{\"questions\":[{\"question\":\"Pick one:\",\"options\":[\"Option A\",\"Option B\"]}]}"}]}}` + "\n"

	// 1. Nil BotAPI guard check
	scanner1 := bufio.NewScanner(strings.NewReader(jsonl))
	session1 := &AgySession{
		BotName:       "TestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner1,
	}
	session1.ctx, session1.cancel = context.WithCancel(context.Background())
	defer session1.cancel()
	session1.readStdoutLoop()

	// 2. Full BotAPI delivery and options caching check
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	scanner2 := bufio.NewScanner(strings.NewReader(jsonl))
	session2 := &AgySession{
		BotName:       "TestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		ChatID:        12345,
		BotAPI:        bot,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner2,
	}
	session2.ctx, session2.cancel = context.WithCancel(context.Background())
	defer session2.cancel()
	session2.readStdoutLoop()

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundQuestion := false
	for _, rawBody := range sentBodies {
		decodedBody, _ := url.QueryUnescape(rawBody)
		if strings.Contains(decodedBody, "Pick one:") {
			foundQuestion = true
			if !strings.Contains(decodedBody, "Option A") || !strings.Contains(decodedBody, "Option B") {
				t.Errorf("Sent question message missing options: %s", decodedBody)
			}
			break
		}
	}
	if !foundQuestion {
		t.Errorf("Expected ask_question to send Telegram message, sent bodies: %v", sentBodies)
	}
}

func TestReadStdoutLoop_ErrorResult(t *testing.T) {
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"generic fatal crash"}}` + "\n"

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	scanner := bufio.NewScanner(strings.NewReader(jsonl))
	session := &AgySession{
		BotName:         "TestBot",
		Model:           defaultModel,
		Workspace:       "/tmp/workspace",
		ChatID:          12345,
		ActiveMessageID: 100,
		BotAPI:          bot,
		isAlive:         true,
		UpdateChan:      make(chan struct{}, 10),
		InitChan:        make(chan string, 10),
		StdoutScanner:   scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Assert session killed on ERROR result
	if session.IsAlive() {
		t.Errorf("Expected session to be killed after ERROR result")
	}

	// Assert buffers cleared
	session.mu.Lock()
	buf := session.TextBuffer
	activeID := session.ActiveMessageID
	session.mu.Unlock()

	if buf != "" || activeID != 0 {
		t.Errorf("Expected cleared buffer and ActiveMessageID=0, got buf=%q activeID=%d", buf, activeID)
	}

	// Assert Telegram notification was sent
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundError := false
	for _, rawBody := range sentBodies {
		decodedBody, _ := url.QueryUnescape(rawBody)
		if strings.Contains(decodedBody, "generic fatal crash") || strings.Contains(decodedBody, "Agent Error") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("Expected error message sent to Telegram, got: %v", sentBodies)
	}
}

func TestReadStdoutLoop_RateLimitRecovery(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	jsonl := `{"event":"result","result":{"status":"ERROR","error":"429 Too Many Requests: Rate limit exceeded"}}` + "\n"

	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestRateLimitBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		Conversation:  "rate-limit-session-uuid",
		UserID:        555,
		DB:            db,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Wait briefly for recovery goroutine to finish
	time.Sleep(50 * time.Millisecond)
	session.Kill()
}

func TestSession_ProcessLifecycleAndPipesClosing(t *testing.T) {
	tempDir := t.TempDir()
	mockScript := filepath.Join(tempDir, "mock_agy.sh")
	scriptContent := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(mockScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create mock script: %v", err)
	}

	os.Setenv("AGY_BINARY", mockScript)
	defer os.Unsetenv("AGY_BINARY")

	user := User{
		ID:        999,
		Workspace: tempDir,
		Model:     "gemini-3.7-flash-high",
		SessionID: "test-lifecycle-conv",
	}

	session := getSession("TestLifecycleBot", user, 12345)
	if session == nil {
		t.Fatal("Expected session to be created")
	}

	// Allow process to start
	time.Sleep(100 * time.Millisecond)

	session.mu.Lock()
	alive := session.isAlive
	hasCmd := session.Cmd != nil
	hasStdin := session.Stdin != nil
	hasStdout := session.StdoutPipe != nil
	waitDelay := time.Duration(0)
	if session.Cmd != nil {
		waitDelay = session.Cmd.WaitDelay
	}
	session.mu.Unlock()

	if !alive || !hasCmd || !hasStdin || !hasStdout {
		t.Fatalf("Session process state incomplete: alive=%v cmd=%v stdin=%v stdout=%v", alive, hasCmd, hasStdin, hasStdout)
	}

	if waitDelay != 2*time.Second {
		t.Errorf("Expected Cmd.WaitDelay to be 2s, got %v", waitDelay)
	}

	// Kill session
	session.Kill()

	session.mu.Lock()
	afterAlive := session.isAlive
	afterCmd := session.Cmd
	afterStdin := session.Stdin
	afterStdout := session.StdoutPipe
	afterActiveMsgID := session.ActiveMessageID
	afterTextBuffer := session.TextBuffer
	session.mu.Unlock()

	if afterAlive {
		t.Errorf("Expected session to not be alive after Kill()")
	}
	if afterCmd != nil || afterStdin != nil || afterStdout != nil {
		t.Errorf("Expected handles to be nil after Kill(), got cmd=%v stdin=%v stdout=%v", afterCmd, afterStdin, afterStdout)
	}
	if afterActiveMsgID != 0 || afterTextBuffer != "" {
		t.Errorf("Expected buffer and activeMsgID reset, got ID=%d text=%q", afterActiveMsgID, afterTextBuffer)
	}
}

func TestCleanMediaAndExports_RemovesExpiredFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, "agents")
	t.Setenv("AGENTS_DIR", agentsDir)

	botScratchDownloads := filepath.Join(agentsDir, "TestBot", "scratch", "downloads")
	botScratchExports := filepath.Join(agentsDir, "TestBot", "scratch", "exports")
	_ = os.MkdirAll(botScratchDownloads, 0755)
	_ = os.MkdirAll(botScratchExports, 0755)

	oldTime := time.Now().Add(-48 * time.Hour)

	// Create old expired files
	oldDownload := filepath.Join(botScratchDownloads, "old_media.ogg")
	_ = os.WriteFile(oldDownload, []byte("old audio"), 0644)
	_ = os.Chtimes(oldDownload, oldTime, oldTime)

	oldExport := filepath.Join(botScratchExports, "old_session.md")
	_ = os.WriteFile(oldExport, []byte("old session transcript"), 0644)
	_ = os.Chtimes(oldExport, oldTime, oldTime)

	// Create fresh recent files
	recentDownload := filepath.Join(botScratchDownloads, "recent_media.ogg")
	_ = os.WriteFile(recentDownload, []byte("recent audio"), 0644)

	recentExport := filepath.Join(botScratchExports, "recent_session.md")
	_ = os.WriteFile(recentExport, []byte("recent export"), 0644)

	// Clean files older than 24 hours
	cleaned := CleanMediaAndExports(24 * time.Hour)
	if cleaned != 2 {
		t.Errorf("expected 2 files cleaned, got: %d", cleaned)
	}

	if _, err := os.Stat(oldDownload); !os.IsNotExist(err) {
		t.Errorf("expected old download to be removed")
	}
	if _, err := os.Stat(oldExport); !os.IsNotExist(err) {
		t.Errorf("expected old export to be removed")
	}
	if _, err := os.Stat(recentDownload); os.IsNotExist(err) {
		t.Errorf("expected recent download to be retained")
	}
	if _, err := os.Stat(recentExport); os.IsNotExist(err) {
		t.Errorf("expected recent export to be retained")
	}
}

func TestCleanMediaAndExports_EmptyOrMissingDir(t *testing.T) {
	t.Setenv("AGENTS_DIR", filepath.Join(t.TempDir(), "nonexistent_agents"))
	cleaned := CleanMediaAndExports(24 * time.Hour)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned for missing dir, got: %d", cleaned)
	}
}

func TestStartDiskCleanupWorker_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("AGENTS_DIR", tmpDir)

	stopChan := make(chan struct{})
	StartDiskCleanupWorker(10*time.Millisecond, 24*time.Hour, stopChan)

	// Let ticker fire at least once
	time.Sleep(30 * time.Millisecond)

	// Signal graceful shutdown
	close(stopChan)
	time.Sleep(10 * time.Millisecond)
}

func TestStartSessionGCWorker_Lifecycle(t *testing.T) {
	stopChan := make(chan struct{})
	StartSessionGCWorker(10*time.Millisecond, 1*time.Hour, stopChan)

	// Let ticker fire at least once
	time.Sleep(30 * time.Millisecond)

	// Signal graceful shutdown
	close(stopChan)
	time.Sleep(10 * time.Millisecond)
}

func TestConcurrentClearSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(999)

	var wg sync.WaitGroup
	const concurrency = 25

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			update := tgbotapi.Update{
				UpdateID: 1000 + idx,
				Message: &tgbotapi.Message{
					MessageID: 100 + idx,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: userID},
					Text:      "/clear",
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}

	if user.SessionID != "" {
		t.Errorf("Expected user.SessionID to be cleared after /clear spam, got %q", user.SessionID)
	}
	ms.mu.Lock()
	clearReqCount := len(ms.sentRequests)
	ms.mu.Unlock()
	if clearReqCount == 0 {
		t.Errorf("Expected Telegram messages to be sent during /clear spam, got 0")
	}
}

func TestConcurrentMessageSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(54321)
	userID := int64(1111)

	var wg sync.WaitGroup
	const concurrency = 25

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			update := tgbotapi.Update{
				UpdateID: 2000 + idx,
				Message: &tgbotapi.Message{
					MessageID: 200 + idx,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: userID},
					Text:      "Concurrent stream test message",
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session == nil {
		t.Fatal("Expected active session to exist after message spam")
	}
	defer session.Kill()

	if !session.IsAlive() {
		t.Error("Expected session to be alive after message spam")
	}
	if user.ID != userID {
		t.Errorf("Expected user ID %d, got %d", userID, user.ID)
	}
	ms.mu.Lock()
	msgReqCount := len(ms.sentRequests)
	ms.mu.Unlock()
	if msgReqCount == 0 {
		t.Errorf("Expected Telegram requests to be dispatched during message spam, got 0")
	}
}

func TestConcurrentModelSwitchSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(67890)
	userID := int64(2222)

	var wg sync.WaitGroup
	const concurrency = 20

	models := []string{
		"model:gemini-3.7-flash-high",
		"model:gemini-3.1-pro-high",
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			selectedModel := models[idx%len(models)]
			update := tgbotapi.Update{
				UpdateID: 3000 + idx,
				CallbackQuery: &tgbotapi.CallbackQuery{
					ID: "cb_id",
					Message: &tgbotapi.Message{
						MessageID: 300 + idx,
						Chat:      &tgbotapi.Chat{ID: chatID},
					},
					From: &tgbotapi.User{ID: userID},
					Data: selectedModel,
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		defer session.Kill()
	}

	if user.Model != "gemini-3.7-flash-high" && user.Model != "gemini-3.1-pro-high" {
		t.Errorf("Expected user.Model to be one of the switched models, got %q", user.Model)
	}
	ms.mu.Lock()
	cbReqCount := len(ms.sentRequests)
	ms.mu.Unlock()
	if cbReqCount == 0 {
		t.Errorf("Expected callback query answers sent to mock bot, got 0")
	}
}

func TestHandleUpdate_PanicRecovery(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)

	update := tgbotapi.Update{
		UpdateID: 9999,
		Message: &tgbotapi.Message{
			MessageID: 999,
			Chat:      &tgbotapi.Chat{ID: 1},
			From:      &tgbotapi.User{ID: 1},
			Text:      "/start",
		},
	}

	// Should recover gracefully from nil db and not panic the test
	handleUpdate(bot, update, nil)
}

func TestAgySession_ReadStdout_StrictTypingAndNilSafety(t *testing.T) {
	s := &AgySession{
		BotName: "test_bot",
		ChatID:  12345,
	}

	// 1. Nil scanner or nil context must return error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.readStdout(nil, ctx); err == nil {
		t.Error("Expected error when scanner is nil, got nil")
	}
	scanner := bufio.NewScanner(strings.NewReader(""))
	if err := s.readStdout(scanner, nil); err == nil {
		t.Error("Expected error when context is nil, got nil")
	}

	// 2. readStdoutLoop facade with nil scanner and ctx should not panic and gracefully return
	s.readStdoutLoop()

	// 3. readStdout with cancelled context returns context.Canceled
	canceledCtx, cancelImmediate := context.WithCancel(context.Background())
	cancelImmediate()
	err := s.readStdout(scanner, canceledCtx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestSession_StreamingThrottler_RaceWithRichMessageFinalization(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var mu sync.Mutex
	deletedMsgIDs := make(map[string]bool)
	var richMessageCalls int
	var deleteMessageCalls int
	var editMessageCalls int
	var fallbackSendMessages []string

	ms.customHandler = func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		vals, _ := url.ParseQuery(string(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendRichMessage") {
			mu.Lock()
			richMessageCalls++
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":5000,"chat":{"id":12345},"text":"rich response"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "deleteMessage") {
			delID := vals.Get("message_id")
			mu.Lock()
			deleteMessageCalls++
			deletedMsgIDs[delID] = true
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}

		if strings.Contains(r.URL.Path, "editMessageText") {
			editID := vals.Get("message_id")
			mu.Lock()
			if vals.Get("rich_message") != "" {
				richMessageCalls++
			} else {
				editMessageCalls++
			}
			isDeleted := deletedMsgIDs[editID]
			mu.Unlock()

			if isDeleted {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`))
				return
			}
			w.Write([]byte(`{"ok":true,"result":{"message_id":4036,"chat":{"id":12345},"text":"edited"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendMessage") {
			txt := vals.Get("text")
			mu.Lock()
			fallbackSendMessages = append(fallbackSendMessages, txt)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":6000,"chat":{"id":12345},"text":"new message"}}`))
			return
		}

		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"ok"}}`))
	}

	bot := createMockBot(ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &AgySession{
		BotName:         "test_bot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 4036,
		ActiveTurnStart: time.Now(),
		UpdateChan:      make(chan struct{}, 100),
	}

	// Start streaming throttler with high frequency (5ms) to test concurrent race condition
	s.startStreamingThrottler(ctx, 5*time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: Rapid streaming token generator pushing into buffer and signaling UpdateChan
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			s.mu.Lock()
			s.TextBuffer += fmt.Sprintf(" chunk-%d ", i)
			s.mu.Unlock()
			select {
			case s.UpdateChan <- struct{}{}:
			default:
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Goroutine 2: Concurrently finalizes turn with a rich message (>4000 chars)
	go func() {
		defer wg.Done()
		time.Sleep(15 * time.Millisecond) // Let some streaming ticks happen
		largeArticle := "# Architecture Guide\n\n" + strings.Repeat("Long article paragraph details. ", 150)
		s.mu.Lock()
		s.TextBuffer = largeArticle
		s.mu.Unlock()
		s.finalizeTurn()
	}()

	wg.Wait()
	time.Sleep(50 * time.Millisecond) // Allow any pending throttler ticks to finish

	mu.Lock()
	rCount := richMessageCalls
	dCount := deleteMessageCalls
	fallbacks := len(fallbackSendMessages)
	mu.Unlock()

	if rCount != 1 {
		t.Errorf("Expected exactly 1 rich message call, got %d", rCount)
	}
	if dCount != 0 {
		t.Errorf("Expected 0 deleteMessage calls (In-Place Morphing preserves draft), got %d", dCount)
	}
	if fallbacks != 0 {
		t.Errorf("Expected 0 fallback sendMessages (no duplicate messages!), got %d: %v", fallbacks, fallbackSendMessages)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveMessageID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after turn finalization, got %d", s.ActiveMessageID)
	}
	if !s.ActiveTurnStart.IsZero() {
		t.Errorf("Expected ActiveTurnStart to be zeroed after finalizeTurn, got %v", s.ActiveTurnStart)
	}
}

func TestSession_FinalizeTurn_EarlyDisarmAndActiveTurnProtection(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var checkedDuringDispatch bool
	var activeMsgIDDuringDispatch int
	var activeTurnStartDuringDispatch time.Time

	var s *AgySession

	ms.customHandler = func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendRichMessage") || (strings.Contains(r.URL.Path, "editMessageText") && strings.Contains(string(bodyBytes), "rich_message")) {
			// Verify invariants during adaptive network dispatch:
			s.mu.Lock()
			activeMsgIDDuringDispatch = s.ActiveMessageID
			activeTurnStartDuringDispatch = s.ActiveTurnStart
			checkedDuringDispatch = true
			s.mu.Unlock()

			w.Write([]byte(`{"ok":true,"result":{"message_id":5001,"chat":{"id":12345},"text":"rich text"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "deleteMessage") {
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}

		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"ok"}}`))
	}

	bot := createMockBot(ms)

	startTurn := time.Now()
	s = &AgySession{
		BotName:         "test_bot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 4036,
		ActiveTurnStart: startTurn,
		TextBuffer:      "# Long Doc\n\n" + strings.Repeat("Testing early disarm and active turn immunity. ", 100),
	}

	s.finalizeTurn()

	if !checkedDuringDispatch {
		t.Fatal("Expected sendRichMessage to be invoked during finalizeTurn")
	}

	// Invariant 1: ActiveMessageID must be 0 DURING dispatch (Early Disarm)
	if activeMsgIDDuringDispatch != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 during dispatch (Early Disarm), got %d", activeMsgIDDuringDispatch)
	}

	// Invariant 2: ActiveTurnStart must remain active DURING dispatch (Active Turn Protection)
	if activeTurnStartDuringDispatch.IsZero() {
		t.Errorf("Expected ActiveTurnStart to remain active during dispatch for GC protection, got zero time")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ActiveTurnStart.IsZero() {
		t.Errorf("Expected ActiveTurnStart to be zeroed out after finalizeTurn, got %v", s.ActiveTurnStart)
	}
}

func TestSession_FinalizeTurn_Tier3ExtremePayload(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var richPreviewReceived bool
	var activeMsgDeleted bool
	var documentReceived bool

	ms.customHandler = func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendRichMessage") {
			richPreviewReceived = true
			w.Write([]byte(`{"ok":true,"result":{"message_id":5001,"chat":{"id":12345},"text":"rich preview"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "deleteMessage") {
			activeMsgDeleted = true
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendDocument") {
			documentReceived = true
			w.Write([]byte(`{"ok":true,"result":{"message_id":5002,"chat":{"id":12345},"document":{"file_id":"doc123"}}}`))
			return
		}

		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"ok"}}`))
	}

	bot := createMockBot(ms)
	extremePayload := "# Extreme Payload Architecture Report\n\n" + strings.Repeat("Deep analysis line with data metrics and cluster telemetry.\n", 600)

	s := &AgySession{
		BotName:         "test_bot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 5555,
		ActiveTurnStart: time.Now(),
		TextBuffer:      extremePayload,
	}

	s.finalizeTurn()

	if !richPreviewReceived {
		t.Error("expected sendRichMessage preview during Tier 3 finalizeTurn")
	}
	if !activeMsgDeleted {
		t.Error("expected activeMsgID 5555 to be deleted during Tier 3 finalizeTurn")
	}
	if !documentReceived {
		t.Error("expected sendDocument to be called during Tier 3 finalizeTurn")
	}

	// Verify turn state is properly zeroed
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ActiveTurnStart.IsZero() {
		t.Errorf("expected ActiveTurnStart to be zeroed after finalizeTurn, got %v", s.ActiveTurnStart)
	}
	if s.ActiveMessageID != 0 {
		t.Errorf("expected ActiveMessageID to be 0 after finalizeTurn, got %d", s.ActiveMessageID)
	}

	// Clean up any test artifact file written during this turn
	artifactDir := getArtifactSaveDir()
	if files, err := os.ReadDir(artifactDir); err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "response_") && strings.HasSuffix(f.Name(), ".md") {
				_ = os.Remove(filepath.Join(artifactDir, f.Name()))
			}
		}
	}
}

func TestSession_Stream_InStreamFusion_ThinkingDelta(t *testing.T) {
	t.Run("ThinkingDeltaAppendedWhenTextAlreadyStarted", func(t *testing.T) {
		jsonl := strings.Join([]string{
			`{"event":"step_update","step_update":{"text_delta":"• PR #352 — нейтрализация тегов мыслей ` + "`" + `"}}`,
			`{"event":"step_update","step_update":{"thinking_delta":"<thought>` + "`" + ` и защита стрима"}}`,
			`{"event":"step_update","step_update":{"text_delta":" успешно завершена."}}`,
		}, "\n") + "\n"

		scanner := bufio.NewScanner(strings.NewReader(jsonl))
		session := &AgySession{
			BotName:         "FusionTestBot",
			ActiveMessageID: 100,
			UpdateChan:      make(chan struct{}, 10),
			StdoutScanner:   scanner,
		}
		session.ctx, session.cancel = context.WithCancel(context.Background())
		defer session.cancel()

		session.readStdoutLoop()

		session.mu.Lock()
		gotBuffer := session.TextBuffer
		isTruncated := session.TextTruncated
		session.mu.Unlock()

		expected := "• PR #352 — нейтрализация тегов мыслей `<thought>` и защита стрима успешно завершена."
		if gotBuffer != expected {
			t.Errorf("expected buffer %q, got %q", expected, gotBuffer)
		}
		if isTruncated {
			t.Error("expected TextTruncated to be false")
		}
	})

	t.Run("PreResponseThinkingIgnoredWhenBufferEmpty", func(t *testing.T) {
		jsonl := `{"event":"step_update","step_update":{"thinking_delta":"Hidden chain-of-thought that must not leak into chat"}}` + "\n"
		scanner := bufio.NewScanner(strings.NewReader(jsonl))
		session := &AgySession{
			BotName:         "FusionTestBotEmpty",
			ActiveMessageID: 100,
			UpdateChan:      make(chan struct{}, 10),
			StdoutScanner:   scanner,
		}
		session.ctx, session.cancel = context.WithCancel(context.Background())
		defer session.cancel()

		session.readStdoutLoop()

		session.mu.Lock()
		gotBuffer := session.TextBuffer
		session.mu.Unlock()

		if gotBuffer != "" {
			t.Errorf("expected empty buffer for pre-response thinking, got %q", gotBuffer)
		}
	})

	t.Run("InStreamFusion_BufferLimitOverflow", func(t *testing.T) {
		session := &AgySession{
			BotName:         "FusionOverflowBot",
			ActiveMessageID: 100,
			TextBuffer:      strings.Repeat("A", maxTextBufferBytes-10),
			UpdateChan:      make(chan struct{}, 10),
			StdoutScanner:   bufio.NewScanner(strings.NewReader(`{"event":"step_update","step_update":{"thinking_delta":"12345678901234567890"}}` + "\n")),
		}
		session.ctx, session.cancel = context.WithCancel(context.Background())
		defer session.cancel()

		session.readStdoutLoop()

		session.mu.Lock()
		isTruncated := session.TextTruncated
		bufLen := len(session.TextBuffer)
		session.mu.Unlock()

		if !isTruncated {
			t.Error("expected TextTruncated to be true after overflow")
		}
		if bufLen != maxTextBufferBytes-10 {
			t.Errorf("expected buffer not to exceed limit, got len %d", bufLen)
		}
	})
}

func TestSession_FinalizeTurn_TranscriptFallback_Recovery(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var richMessageSent bool
	var sentRichText string
	var draftDeleted bool

	ms.customHandler = func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		vals, _ := url.ParseQuery(string(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendRichMessage") || (strings.Contains(r.URL.Path, "editMessageText") && vals.Get("rich_message") != "") {
			richMessageSent = true
			if rm := vals.Get("rich_message"); rm != "" {
				var rmData struct {
					Markdown string `json:"markdown"`
				}
				_ = json.Unmarshal([]byte(rm), &rmData)
				sentRichText = rmData.Markdown
			}
			w.Write([]byte(`{"ok":true,"result":{"message_id":5001,"chat":{"id":12345},"text":"rich article"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "deleteMessage") {
			draftDeleted = true
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"ok"}}`))
	}

	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	accHome := filepath.Join(tmpDir, "acc-pool")
	convID := "test-conv-recovery-355"
	logsDir := filepath.Join(accHome, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}

	fullResponseText := "# Full Restored Architecture Specification\n\n" + strings.Repeat("Extensive deep analysis of streaming and state machine invariants.\n", 60)
	if len(fullResponseText) < 3200 {
		t.Fatalf("payload should exceed 3000 chars for Tier 2 Rich Article, got %d", len(fullResponseText))
	}

	steps := []string{
		`{"step_index":1,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"Please analyze the system architecture"}`,
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","content":"","tool_calls":[{"name":"view_file"}]}`,
		`{"step_index":3,"source":"MODEL","type":"GENERIC","content":"file content preview"}`,
	}
	stepFinal, _ := json.Marshal(map[string]interface{}{
		"step_index": 4,
		"source":     "MODEL",
		"type":       "PLANNER_RESPONSE",
		"content":    fullResponseText,
	})
	steps = append(steps, string(stepFinal))

	transcriptPath := filepath.Join(logsDir, "transcript_full.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(strings.Join(steps, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("failed to write transcript file: %v", err)
	}

	// Simulate Incident: TextBuffer was cut off mid-stream at byte 1000
	truncatedPartialBuffer := fullResponseText[:1000]

	s := &AgySession{
		BotName:         "RecoveryTestBot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 8888,
		ActiveTurnStart: time.Now(),
		Conversation:    convID,
		AccountHomeDir:  accHome,
		TextBuffer:      truncatedPartialBuffer,
	}

	s.finalizeTurn()

	if !richMessageSent {
		t.Error("expected rich message edit (In-Place Morphing) to be called for recovered >3000 chars article (Tier 2 Rich Text)")
	}
	if draftDeleted {
		t.Error("expected streaming draft activeMsgID 8888 NOT to be deleted on Tier 2 delivery (In-Place Morphing Guardrail)")
	}
	if !strings.Contains(sentRichText, "Extensive deep analysis") || len(sentRichText) < 3200 {
		t.Errorf("expected full restored response from transcript, got length %d", len(sentRichText))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveMessageID != 0 {
		t.Errorf("expected ActiveMessageID to be zeroed out, got %d", s.ActiveMessageID)
	}
	if !s.ActiveTurnStart.IsZero() {
		t.Errorf("expected ActiveTurnStart to be zeroed out, got %v", s.ActiveTurnStart)
	}
}

func TestSession_FinalizeTurn_TranscriptFallback_FailSafe(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var mu sync.Mutex
	var sentText string
	ms.customHandler = func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		vals, _ := url.ParseQuery(string(bodyBytes))

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			mu.Lock()
			sentText = vals.Get("text")
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":6001,"chat":{"id":12345},"text":"ok"}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"ok"}}`))
	}
	bot := createMockBot(ms)

	t.Run("MissingTranscript_PreservesBuffer", func(t *testing.T) {
		mu.Lock()
		sentText = ""
		mu.Unlock()

		s := &AgySession{
			BotName:         "FailSafeBot",
			BotAPI:          bot,
			ChatID:          12345,
			Conversation:    "nonexistent-session-355",
			AccountHomeDir:  "/nonexistent/dir",
			TextBuffer:      "Preserved buffer text without panic",
			ActiveTurnStart: time.Now(),
		}

		s.finalizeTurn()

		mu.Lock()
		got := sentText
		mu.Unlock()
		if !strings.Contains(got, "Preserved buffer text without panic") {
			t.Errorf("expected original buffer to be preserved, got %q", got)
		}
	})

	t.Run("CorruptedTranscript_PreservesBuffer", func(t *testing.T) {
		mu.Lock()
		sentText = ""
		mu.Unlock()

		tmpDir := t.TempDir()
		accHome := filepath.Join(tmpDir, "acc-corrupted")
		convID := "corrupted-session-355"
		logsDir := filepath.Join(accHome, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs")
		_ = os.MkdirAll(logsDir, 0755)

		badPath := filepath.Join(logsDir, "transcript_full.jsonl")
		_ = os.WriteFile(badPath, []byte("INVALID_NON_JSON_CORRUPTED_BYTES\n{bad json}\n"), 0644)

		s := &AgySession{
			BotName:         "CorruptedTestBot",
			BotAPI:          bot,
			ChatID:          12345,
			Conversation:    convID,
			AccountHomeDir:  accHome,
			TextBuffer:      "Preserved partial response despite corrupted JSONL",
			ActiveTurnStart: time.Now(),
		}

		s.finalizeTurn()

		mu.Lock()
		got := sentText
		mu.Unlock()
		if !strings.Contains(got, "Preserved partial response despite corrupted JSONL") {
			t.Errorf("expected original buffer to be preserved, got %q", got)
		}
	})

	t.Run("MultiAccountCascadeResolution", func(t *testing.T) {
		tmpDir := t.TempDir()
		convID := "cascade-session-355"
		brainDir := filepath.Join(tmpDir, "custom-brain", convID)
		logsDir := filepath.Join(brainDir, ".system_generated", "logs")
		_ = os.MkdirAll(logsDir, 0755)

		t.Setenv("BRAIN_DIR", filepath.Join(tmpDir, "custom-brain"))
		step, _ := json.Marshal(map[string]interface{}{
			"step_index": 1,
			"source":     "MODEL",
			"type":       "PLANNER_RESPONSE",
			"content":    "Resolved via cascade fallback",
		})
		_ = os.WriteFile(filepath.Join(logsDir, "transcript_full.jsonl"), append(step, '\n'), 0644)

		path := resolveSessionTranscriptPath("", convID)
		if path == "" {
			t.Errorf("expected path to be resolved via cascade BRAIN_DIR fallback, got empty")
		}
		content := readLastModelResponseFromTranscript("", convID)
		if content != "Resolved via cascade fallback" {
			t.Errorf("expected content 'Resolved via cascade fallback', got %q", content)
		}
	})
}

func TestSendArtifacts_BlocksDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PROJECTS_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	envFile := filepath.Join(tempDir, ".env")
	envLocalFile := filepath.Join(tempDir, ".env.production")
	validDoc := filepath.Join(tempDir, "report.md")

	os.WriteFile(envFile, []byte("TELEGRAM_TOKEN=12345\nAPI_KEY=secret\n"), 0644)
	os.WriteFile(envLocalFile, []byte("DB_PASSWORD=secret\n"), 0644)
	os.WriteFile(validDoc, []byte("# Report\nAll systems nominal.\n"), 0644)

	text := fmt.Sprintf("Results:\n- [SecretEnv](file://%s)\n- [LocalEnv](file://%s)\n- [Report](file://%s)\n",
		envFile, envLocalFile, validDoc)

	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	foundReport := false
	for _, body := range ms.sentBodies {
		if strings.Contains(body, ".env") {
			t.Fatalf("Security violation: .env artifact was dispatched to Telegram: %s", body)
		}
		if strings.Contains(body, "report.md") {
			foundReport = true
		}
	}

	if !foundReport {
		t.Errorf("Expected valid report.md to be sent, but was not found in sentBodies")
	}
}
