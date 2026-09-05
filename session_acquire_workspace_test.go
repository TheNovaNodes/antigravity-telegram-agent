package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
// if the new replacement session fails to start (Fixes #188).
func TestHandleClearCommand_ReplaceFailure_DBUnchanged(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	t.Setenv("AGY_BINARY", "/nonexistent/path/to/binary")

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

	handleClearCommand(bot, chatID, userID, botName, user, db)

	var currentSession string
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&currentSession)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}

	if currentSession != "active-uuid-999" {
		t.Errorf("Expected DB session_id to remain active-uuid-999, got %s", currentSession)
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
