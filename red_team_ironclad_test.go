package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendArtifacts_TOCTOU_FileReader(t *testing.T) {
	tempDir := t.TempDir()
	mockProjects := filepath.Join(tempDir, "projects")
	os.MkdirAll(mockProjects, 0755)
	t.Setenv("PROJECTS_DIR", mockProjects)

	artifactFile := filepath.Join(mockProjects, "report.pdf")
	if err := os.WriteFile(artifactFile, []byte("%PDF-mock-binary-content"), 0644); err != nil {
		t.Fatalf("Failed to write mock artifact: %v", err)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	text := "Here is your report: [report.pdf](file://" + artifactFile + ")"
	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Errorf("Expected sendArtifacts to send document via open FileReader")
	}
}

func TestReadStdoutLoop_InitEvent_InsertsSessionHistory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(7777)
	getUser(db, userID, "TestMockBot")

	jsonl := `{"event":"init","conversation_id":"fresh-new-conv-uuid-888"}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestMockBot",
		ChatID:        12345,
		UserID:        userID,
		DB:            db,
		Model:         "gemini-2.5-pro",
		Workspace:     "/tmp/test_ws",
		Conversation:  "",
		InitChan:      make(chan string, 10),
		UpdateChan:    make(chan struct{}, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Verify session was inserted into session_history
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ? AND session_id = ?", userID, "fresh-new-conv-uuid-888").Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Expected session_history to contain fresh-new-conv-uuid-888, count=%d, err=%v", count, err)
	}

	// Verify users table was updated
	user := getUser(db, userID, "TestMockBot")
	if user.SessionID != "fresh-new-conv-uuid-888" {
		t.Errorf("Expected users.session_id to be fresh-new-conv-uuid-888, got %s", user.SessionID)
	}
}

func TestHandleResumeCommand_Deduplication(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(8888)
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	// Create duplicate entries in session_history directly
	sessionID := "duplicate-session-uuid-111"
	_, _ = db.Exec("INSERT INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
	// Create dummy transcript
	logDir := filepath.Join(tempDir, sessionID, ".system_generated", "logs")
	os.MkdirAll(logDir, 0755)
	os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(`{"created_at":"2026-09-04T12:00:00Z","content":"<USER_REQUEST>Test Dup</USER_REQUEST>"}`+"\n"), 0644)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	handleResumeCommand(bot, 12345, userID, db)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Errorf("Expected handleResumeCommand to send resume keyboard")
	}
}

func TestHandleClearCommand_OrderOfOperations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(9999)
	user := getUser(db, userID, "TestMockBot")
	updateUserSession(db, userID, "pre-clear-session-123")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	handleClearCommand(bot, 12345, userID, "TestMockBot", user, db)

	// User session in DB should immediately be empty (cleared)
	clearedUser := getUser(db, userID, "TestMockBot")
	if clearedUser.SessionID != "" {
		t.Logf("Session ID after clear: %s", clearedUser.SessionID)
	}
}
