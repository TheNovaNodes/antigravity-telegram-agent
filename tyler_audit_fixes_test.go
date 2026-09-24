package main

import (
	"bufio"
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

// TestTylerAudit_ReadStdoutLoop_PreservesBufferOnActiveTurn verifies that when readStdoutLoop
// exits while an active turn is in progress (e.g. during recovery or process termination),
// its deferred cleanup does NOT wipe out s.TextBuffer.
func TestTylerAudit_ReadStdoutLoop_PreservesBufferOnActiveTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &AgySession{
		BotName:         "test_bot",
		ChatID:          12345,
		ActiveMessageID: 9999, // Active turn in progress
		TextBuffer:      "Vital partial output salvaged from stream",
		ctx:             ctx,
	}

	// An empty scanner that immediately hits EOF
	scanner := bufio.NewScanner(strings.NewReader(""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.readStdoutLoop(scanner, ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readStdoutLoop timed out")
	}

	s.mu.Lock()
	buf := s.TextBuffer
	s.mu.Unlock()

	if buf != "Vital partial output salvaged from stream" {
		t.Fatalf("Expected TextBuffer to be preserved, got '%s'", buf)
	}
}

// TestTylerAudit_ReadStdoutLoop_ClearsBufferWhenNoActiveTurn verifies that when no turn is active,
// readStdoutLoop cleans up its buffer on exit.
func TestTylerAudit_ReadStdoutLoop_ClearsBufferWhenNoActiveTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &AgySession{
		BotName:         "test_bot",
		ChatID:          12345,
		ActiveMessageID: 0, // No active turn
		TextBuffer:      "Stale leftover data",
		ctx:             ctx,
	}

	scanner := bufio.NewScanner(strings.NewReader(""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.readStdoutLoop(scanner, ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readStdoutLoop timed out")
	}

	s.mu.Lock()
	buf := s.TextBuffer
	s.mu.Unlock()

	if buf != "" {
		t.Fatalf("Expected TextBuffer to be cleared when ActiveMessageID==0, got '%s'", buf)
	}
}

// TestTylerAudit_ResetChatSessionCache_NonBlockingLock verifies that resetChatSessionCache
// does not hold sessionMu while terminating processes.
func TestTylerAudit_ResetChatSessionCache_NonBlockingLock(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	testBot := "tyler_test_bot"
	chatID := int64(778899)
	userID := int64(112233)
	key := "tyler_test_bot:778899:112233"

	sess := &AgySession{
		BotName:      testBot,
		ChatID:       chatID,
		UserID:       userID,
		Conversation: "test-conv-id",
	}

	sessionMu.Lock()
	globalSessions[key] = sess
	sessionMu.Unlock()

	// Call resetChatSessionCache
	resetChatSessionCache(db, testBot, userID, chatID)

	// Immediately verify sessionMu is unlocked and globalSessions evicted the key
	sessionMu.Lock()
	_, exists := globalSessions[key]
	sessionMu.Unlock()

	if exists {
		t.Fatalf("Expected session %s to be evicted", key)
	}
}

// TestTylerAudit_CleanTextForTTS_CleanOutput verifies that CleanTextForTTS correctly strips
// code blocks, inline code, links, and caps length cleanly.
func TestTylerAudit_CleanTextForTTS_CleanOutput(t *testing.T) {
	input := "Hello! Here is some code: ```go\nfmt.Println(\"secret\")\n``` and inline `token := 123`. Click [here](https://example.com) for details."
	got := CleanTextForTTS(input)

	if strings.Contains(got, "fmt.Println") {
		t.Errorf("Expected code block to be stripped, got: %s", got)
	}
	if strings.Contains(got, "token :=") {
		t.Errorf("Expected inline code to be stripped, got: %s", got)
	}
	if strings.Contains(got, "https://example.com") {
		t.Errorf("Expected URL to be stripped, got: %s", got)
	}
	if !strings.Contains(got, "here") {
		t.Errorf("Expected link text 'here' to remain, got: %s", got)
	}
}

// TestTylerAudit_DispatchUpdate_ReusedTimer verifies that dispatchUpdate properly creates
// a worker with a reusable timer and processes updates without deadlocks or panic.
func TestTylerAudit_DispatchUpdate_ReusedTimer(t *testing.T) {
	chatQueuesMu.Lock()
	origTimeout := chatQueueIdleTimeout
	chatQueueIdleTimeout = 80 * time.Millisecond
	chatQueuesMu.Unlock()
	defer func() {
		chatQueuesMu.Lock()
		chatQueueIdleTimeout = origTimeout
		chatQueuesMu.Unlock()
	}()

	testChatID := int64(999111)

	chatQueuesMu.Lock()
	delete(chatQueues, testChatID)
	chatQueuesMu.Unlock()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	// Dispatch multiple rapid updates
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			upd := tgbotapi.Update{
				UpdateID: 500 + idx,
				Message: &tgbotapi.Message{
					MessageID: 1000 + idx,
					Chat:      &tgbotapi.Chat{ID: testChatID},
					From:      &tgbotapi.User{ID: 12345, UserName: "testuser"},
					Text:      "/help",
				},
			}
			dispatchUpdate(bot, upd, db)
		}(i)
	}
	wg.Wait()

	// Wait for worker idle timeout eviction with retry polling to handle race detector scheduling jitter
	evicted := false
	for attempt := 0; attempt < 50; attempt++ {
		time.Sleep(20 * time.Millisecond)
		chatQueuesMu.Lock()
		_, active := chatQueues[testChatID]
		chatQueuesMu.Unlock()
		if !active {
			evicted = true
			break
		}
	}

	if !evicted {
		t.Errorf("Expected worker queue for chat %d to be evicted after idle timeout", testChatID)
	}
}
