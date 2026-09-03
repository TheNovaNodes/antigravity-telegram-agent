package main

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

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

	// Runs without BotAPI (nil guard check)
	session.readStdoutLoop()
}

func TestReadStdoutLoop_ErrorResult(t *testing.T) {
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"generic error message"}}` + "\n"

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
