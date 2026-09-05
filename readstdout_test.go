package main

import (
	"bufio"
	"context"
	"net/url"
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
	session1.readStdoutLoop(scanner1, session1.ctx)

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
	session2.readStdoutLoop(scanner2, session2.ctx)

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

	session.readStdoutLoop(scanner, session.ctx)

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
