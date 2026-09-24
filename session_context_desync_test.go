package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func getCounterValue(counter prometheus.Counter) float64 {
	var m dto.Metric
	if err := counter.Write(&m); err != nil {
		return 0
	}
	if m.Counter == nil {
		return 0
	}
	return m.Counter.GetValue()
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
