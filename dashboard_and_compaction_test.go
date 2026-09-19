package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleStartCommand_DisplaysAccountAndNewSessionButtons(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(777)
	user := getUser(db, userID, "TestMockBot")
	user.SessionID = "11111111-2222-3333-4444-555555555555"

	// Mock account pool
	pool, err := NewAccountPool(filepath.Join(tempDir, "pool"))
	if err != nil {
		t.Fatalf("NewAccountPool failed: %v", err)
	}
	acc := &Account{
		ID:      "acc-dashboard-test",
		Email:   "test@example.com",
		HomeDir: tempDir,
		State:   StateActive,
	}
	pool.accounts[acc.ID] = acc
	pool.activeChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = nil }()

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected Telegram message to be sent for handleStartCommand")
	}

	var foundAccount, foundNewSessionBtn, foundAccountsBtn bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Account:* `acc-dashboard-test`") {
			foundAccount = true
		}
		if strings.Contains(unescaped, "🆕 New Session") {
			foundNewSessionBtn = true
		}
		if strings.Contains(unescaped, "👥 Accounts") {
			foundAccountsBtn = true
		}
	}

	if !foundAccount {
		t.Errorf("Expected start message to contain Account 'acc-dashboard-test', got: %v", sentBodies)
	}
	if !foundNewSessionBtn {
		t.Errorf("Expected start message keyboard to contain '🆕 New Session', got: %v", sentBodies)
	}
	if !foundAccountsBtn {
		t.Errorf("Expected start message keyboard to contain '👥 Accounts', got: %v", sentBodies)
	}
}

func TestHandleClearCommand_FreshSessionUX(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(777)
	user := getUser(db, userID, "TestMockBot")
	user.SessionID = "22222222-3333-4444-5555-666666666666"

	handleClearCommand(bot, chatID, userID, "TestMockBot", user, db)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected message sent for handleClearCommand")
	}

	foundFreshSession := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Fresh session initiated") {
			foundFreshSession = true
			break
		}
	}

	if !foundFreshSession {
		t.Errorf("Expected fresh session initiated text in response, got: %v", sentBodies)
	}
}

func TestGetCompactionHint(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	convID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	logsDir := filepath.Join(tempDir, convID, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create test logs dir: %v", err)
	}
	transcriptPath := filepath.Join(logsDir, "transcript.jsonl")

	// Case 1: Small transcript (< 500 KB) and no stream recovery
	smallData := []byte("{\"step\": 1, \"content\": \"hello\"}\n")
	if err := os.WriteFile(transcriptPath, smallData, 0644); err != nil {
		t.Fatalf("Failed to write small transcript: %v", err)
	}

	hint := getCompactionHint(convID, false)
	if hint != "" {
		t.Errorf("Expected empty hint for small transcript without stream recovery, got: %q", hint)
	}

	// Case 2: Small transcript with stream recovery
	hintRecovery := getCompactionHint(convID, true)
	if !strings.Contains(hintRecovery, "Network stream interruption detected") {
		t.Errorf("Expected stream recovery warning, got: %q", hintRecovery)
	}
	if !strings.Contains(hintRecovery, "/export") || !strings.Contains(hintRecovery, "🆕 New Session") {
		t.Errorf("Expected recommendation for export and new session, got: %q", hintRecovery)
	}

	// Case 3: Transcript >= 500 KB (512 KB)
	largeData := make([]byte, 512*1024)
	for i := range largeData {
		largeData[i] = 'A'
	}
	if err := os.WriteFile(transcriptPath, largeData, 0644); err != nil {
		t.Fatalf("Failed to write large transcript: %v", err)
	}

	hintLarge := getCompactionHint(convID, false)
	if !strings.Contains(hintLarge, "Session transcript reached 512 KB") {
		t.Errorf("Expected size notice (512 KB), got: %q", hintLarge)
	}
	if !strings.Contains(hintLarge, "/export") || !strings.Contains(hintLarge, "🆕 New Session") {
		t.Errorf("Expected export and new session advice, got: %q", hintLarge)
	}

	// Case 4: Transcript >= 500 KB with stream recovery (size takes precedence / clear messaging)
	hintLargeWithRecovery := getCompactionHint(convID, true)
	if !strings.Contains(hintLargeWithRecovery, "Session transcript reached 512 KB") {
		t.Errorf("Expected size notice (512 KB) even with stream recovery, got: %q", hintLargeWithRecovery)
	}
}

func TestHandleStartCommand_PinnedAccountDisplay(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(99999)
	userID := int64(888)
	user := getUser(db, userID, "TestMockBot")

	pool, err := NewAccountPool(filepath.Join(tempDir, "pool"))
	if err != nil {
		t.Fatalf("NewAccountPool failed: %v", err)
	}
	acc := &Account{
		ID:      "acc-pinned-vip",
		Email:   "vip@example.com",
		HomeDir: tempDir,
		State:   StateActive,
	}
	pool.accounts[acc.ID] = acc
	pool.activeChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	pool.pinnedChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = nil }()

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundPinned := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Account:* `acc-pinned-vip 🔒`") {
			foundPinned = true
			break
		}
	}

	if !foundPinned {
		t.Errorf("Expected pinned account indicator 'acc-pinned-vip 🔒', got: %v", sentBodies)
	}
}
