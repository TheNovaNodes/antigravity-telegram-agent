package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open memory db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '',
		model TEXT DEFAULT 'gemini-3.1-pro-high',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL
	)`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS session_history (
		user_id INTEGER,
		session_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, session_id)
	)`)
	if err != nil {
		t.Fatalf("Failed to create session_history table: %v", err)
	}
	return db
}

func TestGetAgentsDir(t *testing.T) {
	dir := getAgentsDir()
	home, err := os.UserHomeDir()
	expected := "/root/.agents"
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

func TestUpdateChan(t *testing.T) {
	session := &AgySession{
		UpdateChan: make(chan struct{}, 1),
	}
	// Test sending to UpdateChan does not block (event-driven logic)
	select {
	case session.UpdateChan <- struct{}{}:
	case <-time.After(1 * time.Second):
		t.Error("Sending to UpdateChan blocked")
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

	session1 := replaceSession(db, botName, user, "uuid-1", "test-model", "/tmp/workspace", chatID)
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
	session2 := replaceSession(db, botName, user, "uuid-2", "test-model", "/tmp/new_workspace", chatID)

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
