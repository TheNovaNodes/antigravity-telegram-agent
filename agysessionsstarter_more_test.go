package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

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
	sendArtifacts(nil, 1234, "Generated file: [output](file:///root/.agents/test/output.txt)")
}

func TestHandleUpdate_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	// Should return immediately without doing anything
	handleUpdate(nil, tgbotapi.Update{}, db)
}
