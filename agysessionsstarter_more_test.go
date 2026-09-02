package main

import (
	"os"
	"testing"
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

	// Clean up cmd if it was started
	if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}
}
