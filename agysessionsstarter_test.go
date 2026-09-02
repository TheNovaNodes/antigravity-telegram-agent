package main

import (
	"database/sql"
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
		model TEXT DEFAULT 'gemini-3.7-flash-high',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL
	)`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
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
