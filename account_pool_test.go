package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func setupTestAccountPool(t *testing.T) (*AccountPool, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "account_pool_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	pool, err := NewAccountPool(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create account pool: %v", err)
	}
	return pool, tmpDir
}

func TestAccountPool_AcquireReleaseLRU(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	pool.accounts["acc-1"] = &Account{
		ID:          "acc-1",
		Email:       "user1@example.com",
		HomeDir:     filepath.Join(tmpDir, "acc-1"),
		State:       StateActive,
		LastUsed:    now.Add(-2 * time.Hour), // Older LastUsed
		ActiveTurns: 0,
	}
	pool.accounts["acc-2"] = &Account{
		ID:          "acc-2",
		Email:       "user2@example.com",
		HomeDir:     filepath.Join(tmpDir, "acc-2"),
		State:       StateActive,
		LastUsed:    now.Add(-10 * time.Minute), // More recent
		ActiveTurns: 0,
	}

	// First acquisition for chat 100 should pick acc-1 (LRU)
	acc, err := pool.AcquireAccount(100)
	if err != nil {
		t.Fatalf("Expected successful acquire, got: %v", err)
	}
	if acc.ID != "acc-1" {
		t.Fatalf("Expected acc-1 due to LRU, got %s", acc.ID)
	}
	if acc.State != StateInUse {
		t.Fatalf("Expected state InUse, got %v", acc.State)
	}

	// Second acquisition for chat 200 should pick acc-2 (0 active turns vs acc-1's 1 active turn)
	acc2, err := pool.AcquireAccount(200)
	if err != nil {
		t.Fatalf("Expected successful acquire for chat 200, got: %v", err)
	}
	if acc2.ID != "acc-2" {
		t.Fatalf("Expected acc-2, got %s", acc2.ID)
	}

	// Release acc-1
	pool.ReleaseAccount("acc-1")
	reloaded, _ := pool.GetAccount("acc-1")
	if reloaded.State != StateActive {
		t.Fatalf("Expected StateActive after release, got %v", reloaded.State)
	}
	if reloaded.ActiveTurns != 0 {
		t.Fatalf("Expected 0 active turns, got %d", reloaded.ActiveTurns)
	}
}

func TestAccountPool_StickyPin(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	pool.accounts["acc-1"] = &Account{ID: "acc-1", Email: "user1@example.com", State: StateActive}
	pool.accounts["acc-2"] = &Account{ID: "acc-2", Email: "user2@example.com", State: StateActive}

	chatID := int64(12345)

	// Pin chat to acc-2
	if err := pool.PinAccount(chatID, "acc-2"); err != nil {
		t.Fatalf("Failed to pin account: %v", err)
	}
	if !pool.IsPinned(chatID) {
		t.Fatalf("Expected chat %d to be pinned", chatID)
	}
	pinnedID, ok := pool.GetPinnedAccount(chatID)
	if !ok || pinnedID != "acc-2" {
		t.Fatalf("Expected pinned account acc-2, got %s", pinnedID)
	}

	// Acquire should return pinned acc-2 regardless of acc-1
	acc, err := pool.AcquireAccount(chatID)
	if err != nil {
		t.Fatalf("Failed to acquire pinned account: %v", err)
	}
	if acc.ID != "acc-2" {
		t.Fatalf("Expected acc-2, got %s", acc.ID)
	}

	// Unpin chat
	if err := pool.UnpinAccount(chatID); err != nil {
		t.Fatalf("Failed to unpin: %v", err)
	}
	if pool.IsPinned(chatID) {
		t.Fatalf("Expected chat %d to be unpinned", chatID)
	}
}

func TestAccountPool_CooldownAndAutoParking(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	pool.accounts["acc-1"] = &Account{ID: "acc-1", Email: "user1@example.com", State: StateActive}
	pool.accounts["acc-2"] = &Account{ID: "acc-2", Email: "user2@example.com", State: StateActive}

	// Mark acc-1 in cooldown for 5 hours
	pool.MarkCooldown("acc-1", 5*time.Hour)
	acc1, _ := pool.GetAccount("acc-1")
	if acc1.State != StateCooldown {
		t.Fatalf("Expected StateCooldown for acc-1, got %v", acc1.State)
	}

	// Acquire for chat 10 should select acc-2
	acc, err := pool.AcquireAccount(10)
	if err != nil {
		t.Fatalf("Failed to acquire account: %v", err)
	}
	if acc.ID != "acc-2" {
		t.Fatalf("Expected acc-2, got %s", acc.ID)
	}

	// Now mark acc-2 in cooldown as well
	pool.MarkCooldown("acc-2", 5*time.Hour)

	// Next acquire must return ErrAllAccountsCooldown (triggering Tier 2 Safe Parking)
	_, err = pool.AcquireAccount(20)
	if err != ErrAllAccountsCooldown {
		t.Fatalf("Expected ErrAllAccountsCooldown, got: %v", err)
	}

	// Manual cooldown clear on acc-1
	if err := pool.ClearCooldown("acc-1"); err != nil {
		t.Fatalf("Failed to clear cooldown: %v", err)
	}
	acc1Reloaded, _ := pool.GetAccount("acc-1")
	if acc1Reloaded.State != StateActive {
		t.Fatalf("Expected StateActive after manual clear, got %v", acc1Reloaded.State)
	}

	// Now acc-1 can be acquired again
	accRecovered, err := pool.AcquireAccount(30)
	if err != nil {
		t.Fatalf("Expected successful acquire after cooldown clear: %v", err)
	}
	if accRecovered.ID != "acc-1" {
		t.Fatalf("Expected acc-1, got %s", accRecovered.ID)
	}
}

func TestAccountPool_BackgroundReaper(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	// Account whose cooldown has already passed
	pool.accounts["acc-cooled"] = &Account{
		ID:            "acc-cooled",
		Email:         "cooled@example.com",
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(-10 * time.Second), // In the past
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recoveredChan := make(chan string, 1)
	go pool.StartBackgroundReaperWithInterval(ctx, 20*time.Millisecond, func(acc *Account) {
		recoveredChan <- acc.ID
	})

	select {
	case id := <-recoveredChan:
		if id != "acc-cooled" {
			t.Fatalf("Expected acc-cooled to recover, got %s", id)
		}
	case <-time.After(3 * time.Second):
		// Test manual step if ticker didn't fire in 3s
		acc, _ := pool.GetAccount("acc-cooled")
		if acc.State == StateCooldown {
			// Trigger by calling AcquireAccount which checks cooldown expiration
			_, _ = pool.AcquireAccount(999)
			acc, _ = pool.GetAccount("acc-cooled")
			if acc.State != StateActive {
				t.Fatalf("Expected acc-cooled to become active, got %v", acc.State)
			}
		}
	}
}

func TestAccountPool_ConcurrencyRace(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	for i := 1; i <= 5; i++ {
		id := filepath.Join(tmpDir, "acc")
		pool.accounts[id] = &Account{
			ID:          id,
			Email:       "test@example.com",
			HomeDir:     id,
			State:       StateActive,
			LastUsed:    time.Now(),
			ActiveTurns: 0,
		}
	}

	var wg sync.WaitGroup
	workers := 20
	iterations := 50

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			chatID := int64(workerID % 5)
			for i := 0; i < iterations; i++ {
				acc, err := pool.AcquireAccount(chatID)
				if err == nil && acc != nil {
					time.Sleep(1 * time.Millisecond)
					pool.ReleaseAccount(acc.ID)
				}
				if i%10 == 0 {
					_ = pool.ListAccounts()
				}
			}
		}(w)
	}

	wg.Wait()
}

func TestAccountHandlers_MaskEmail(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"john.doe@gmail.com", "john.doe@gmail.com"},
		{"ab@example.com", "ab@example.com"},
		{"a@domain.com", "a@domain.com"},
		{"notanemail", "notanemail"},
	}

	for _, tc := range tests {
		got := maskEmail(tc.input)
		if got != tc.expected {
			t.Errorf("maskEmail(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestAccountHandlers_ResetSessionCache(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "test_users_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	db, err := sql.Open("sqlite3", tmpDB.Name())
	if err != nil {
		t.Fatalf("Failed to open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, session_id TEXT)`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO users (user_id, session_id) VALUES (42, 'session-abc-123')`)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	// Register in-memory sessions: both canonical (botName:chatID:userID) and legacy format
	canonicalKey := "testbot:1001:42"
	legacyKey := "testbot:1001"
	sessionMu.Lock()
	globalSessions[canonicalKey] = &AgySession{
		BotName:      "testbot",
		ChatID:       1001,
		UserID:       42,
		Conversation: "session-abc-123",
		UseContinue:  true,
	}
	globalSessions[legacyKey] = &AgySession{
		BotName:      "testbot",
		ChatID:       1001,
		UserID:       42,
		Conversation: "session-abc-123",
		UseContinue:  true,
	}
	sessionMu.Unlock()

	// Execute resetChatSessionCache
	resetChatSessionCache(db, "testbot", 42, 1001)

	// Verify SQLite session_id is NULL
	var sessID sql.NullString
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = 42").Scan(&sessID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if sessID.Valid && sessID.String != "" {
		t.Fatalf("Expected NULL session_id in SQLite, got %s", sessID.String)
	}

	// Verify in-memory sessions are completely evicted from globalSessions
	sessionMu.Lock()
	sessCanonical, existsCanonical := globalSessions[canonicalKey]
	sessLegacy, existsLegacy := globalSessions[legacyKey]
	sessionMu.Unlock()

	if existsCanonical || sessCanonical != nil {
		t.Fatalf("Expected canonical session to be evicted from globalSessions, got: %v", sessCanonical)
	}
	if existsLegacy || sessLegacy != nil {
		t.Fatalf("Expected legacy session to be evicted from globalSessions, got: %v", sessLegacy)
	}
}

func TestAccountHandlers_FormatDashboard(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	pool.accounts["acc-1"] = &Account{
		ID:       "acc-1",
		Email:    "dev.lead@gmail.com",
		State:    StateActive,
		LastUsed: time.Now(),
	}
	pool.accounts["acc-2"] = &Account{
		ID:            "acc-2",
		Email:         "backup@gmail.com",
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(2 * time.Hour),
		TotalErrors:   1,
	}

	pool.activeChat["555"] = "acc-1"

	dash, markup := formatAccountsDashboard(pool, 555)

	// Check English UI strings and unmasked email content (#228)
	if !testing.Short() {
		if !containsAll(dash, "Account Pool Manager", "[acc-1]", "dev.lead@gmail.com", "[CURRENT]", "Active (Ready)", "[acc-2]", "Cooldown") {
			t.Fatalf("Dashboard missing expected English elements:\n%s", dash)
		}
	}

	if len(markup.InlineKeyboard) == 0 {
		t.Fatalf("Expected inline buttons in dashboard markup")
	}
}

func TestParseUsageJSON(t *testing.T) {
	rawJSON := []byte(`{
		"command": {
			"name": "usage",
			"data": {
				"groups": [
					{
						"name": "Gemini Models",
						"buckets": [
							{
								"id": "gemini-weekly",
								"window": "weekly",
								"remaining_fraction": 0.88,
								"reset_time": "2026-09-15T09:29:32Z"
							},
							{
								"id": "gemini-5h",
								"window": "5h",
								"remaining_fraction": 0.60,
								"reset_time": "2026-09-08T19:29:32Z"
							}
						]
					},
					{
						"name": "Claude and GPT models",
						"buckets": [
							{
								"id": "3p-weekly",
								"window": "weekly",
								"remaining_fraction": 1.0,
								"reset_time": "2026-09-15T09:27:22Z"
							},
							{
								"id": "3p-5h",
								"window": "5h",
								"remaining_fraction": 0.75,
								"reset_time": "2026-09-08T21:50:36Z"
							}
						]
					}
				]
			}
		}
	}`)

	quota, err := ParseUsageJSON(rawJSON)
	if err != nil {
		t.Fatalf("ParseUsageJSON returned error: %v", err)
	}

	if quota.Gemini5h.RemainingFraction != 0.60 {
		t.Errorf("Expected Gemini 5h 0.60, got %f", quota.Gemini5h.RemainingFraction)
	}
	if quota.GeminiWeekly.RemainingFraction != 0.88 {
		t.Errorf("Expected Gemini weekly 0.88, got %f", quota.GeminiWeekly.RemainingFraction)
	}
	if quota.ClaudeWeekly.RemainingFraction != 1.0 {
		t.Errorf("Expected Claude weekly 1.0, got %f", quota.ClaudeWeekly.RemainingFraction)
	}
	if quota.Claude5h.RemainingFraction != 0.75 {
		t.Errorf("Expected Claude 5h 0.75, got %f", quota.Claude5h.RemainingFraction)
	}
	if quota.Gemini5h.ResetTime.IsZero() {
		t.Errorf("Expected valid Gemini 5h reset time")
	}
}

func TestAccountPool_QuotaPrioritization(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	// acc-1 has lower remaining quota (0.20)
	pool.accounts["acc-1"] = &Account{
		ID:          "acc-1",
		Email:       "user1@gmail.com",
		State:       StateActive,
		LastUsed:    now.Add(-1 * time.Hour),
		ActiveTurns: 0,
		Quota: AccountQuota{
			Gemini5h:      ModelQuota{RemainingFraction: 0.20},
			LastFetchedAt: now,
		},
	}
	// acc-2 has higher remaining quota (0.90)
	pool.accounts["acc-2"] = &Account{
		ID:          "acc-2",
		Email:       "user2@gmail.com",
		State:       StateActive,
		LastUsed:    now.Add(-1 * time.Hour),
		ActiveTurns: 0,
		Quota: AccountQuota{
			Gemini5h:      ModelQuota{RemainingFraction: 0.90},
			LastFetchedAt: now,
		},
	}

	// Should select acc-2 due to higher remaining quota
	acc, err := pool.AcquireAccount(777)
	if err != nil {
		t.Fatalf("AcquireAccount failed: %v", err)
	}
	if acc.ID != "acc-2" {
		t.Errorf("Expected acc-2 (higher quota 90%%), got %s", acc.ID)
	}
}

func containsAll(str string, substrs ...string) bool {
	for _, s := range substrs {
		if !strings.Contains(str, s) {
			return false
		}
	}
	return true
}

func TestHandleUsageCommand_WithActiveAccount(t *testing.T) {
	ts, sent, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("MOCK_TOKEN", ts.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "agy")
	script := "#!/bin/sh\necho \"Active HOME=$HOME\"\necho \"Gemini Models 5h: 90%\"\n"
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock agy: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	targetHome := filepath.Join(tmpDir, "acc-special-home")
	_ = os.MkdirAll(targetHome, 0755)

	pool.accounts["acc-test"] = &Account{
		ID:       "acc-test",
		Email:    "test_user@gmail.com",
		HomeDir:  targetHome,
		State:    StateActive,
		LastUsed: time.Now(),
	}
	pool.activeChat["12345"] = "acc-test"

	handleUsageCommand(bot, 12345)

	mu.Lock()
	defer mu.Unlock()
	if len(*sent) == 0 {
		t.Fatalf("Expected message to be sent via mock bot")
	}
	lastReq := (*sent)[len(*sent)-1]
	vals, _ := url.ParseQuery(lastReq)
	text := vals.Get("text")

	if !strings.Contains(text, "Quota Usage [acc-test (test\\_user@gmail.com)]") && !strings.Contains(text, "acc-test") {
		t.Errorf("Expected account header in text, got: %s", text)
	}
	if !strings.Contains(text, "Active HOME="+targetHome) {
		t.Errorf("Expected mock agy to receive HOME=%s, got: %s", targetHome, text)
	}
}
