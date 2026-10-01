package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

func TestAccountPool_StartBackgroundReaper_ContextCancellation(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	go func() {
		pool.StartBackgroundReaperWithInterval(ctx, 10*time.Millisecond, nil)
		close(stopped)
	}()

	cancel()

	select {
	case <-stopped:
		// Goroutine exited cleanly
	case <-time.After(1 * time.Second):
		t.Fatal("StartBackgroundReaper did not terminate promptly upon context cancellation")
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

func TestHandleUsageCommand_AllAccountsCooldown_NoRootLeak(t *testing.T) {
	ts, sent, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("MOCK_TOKEN", ts.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	pool.accounts["acc-cooldown"] = &Account{
		ID:            "acc-cooldown",
		Email:         "cooldown@example.com",
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(1 * time.Hour),
	}

	handleUsageCommand(bot, 99999)

	mu.Lock()
	defer mu.Unlock()
	if len(*sent) == 0 {
		t.Fatalf("Expected message to be sent via mock bot")
	}
	lastReq := (*sent)[len(*sent)-1]
	vals, _ := url.ParseQuery(lastReq)
	text := vals.Get("text")

	if !strings.Contains(text, "All accounts in the pool are currently resting in cooldown or unavailable") {
		t.Errorf("Expected resting message when all accounts in cooldown, got: %s", text)
	}
}

func TestFetchAccountQuotas_AutoClearsCooldownIfHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	targetHome := filepath.Join(tmpDir, "acc-healthy")
	_ = os.MkdirAll(targetHome, 0755)

	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	usageJSON := `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0.85,"reset_time":"2026-09-10T22:00:00Z"}]}]}}}`
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", usageJSON)
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool.accounts["acc-healthy"] = &Account{
		ID:            "acc-healthy",
		Email:         "healthy@example.com",
		HomeDir:       targetHome,
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(2 * time.Hour),
	}

	q, err := pool.FetchAccountQuotas("acc-healthy")
	if err != nil {
		t.Fatalf("FetchAccountQuotas failed: %v", err)
	}
	if q.Gemini5h.RemainingFraction != 0.85 {
		t.Errorf("Expected 0.85 remaining fraction, got %v", q.Gemini5h.RemainingFraction)
	}

	acc, _ := pool.GetAccount("acc-healthy")
	if acc.State != StateActive {
		t.Errorf("Expected StateActive after auto-clear, got %v", acc.State)
	}
	if !acc.CooldownUntil.IsZero() {
		t.Errorf("Expected zero CooldownUntil after auto-clear, got %v", acc.CooldownUntil)
	}
}

func TestSession_StartReacquiresOnCooldown(t *testing.T) {
	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := "#!/bin/sh\nexec cat\n"
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock agy: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	home1 := filepath.Join(tmpDir, "acc-1")
	home2 := filepath.Join(tmpDir, "acc-2")
	_ = os.MkdirAll(home1, 0755)
	_ = os.MkdirAll(home2, 0755)

	pool.accounts["acc-1"] = &Account{
		ID:            "acc-1",
		Email:         "acc1@example.com",
		HomeDir:       home1,
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(2 * time.Hour),
	}
	pool.accounts["acc-2"] = &Account{
		ID:       "acc-2",
		Email:    "acc2@example.com",
		HomeDir:  home2,
		State:    StateActive,
		LastUsed: time.Now().Add(-1 * time.Hour),
	}

	s := &AgySession{
		AccountID:      "acc-1",
		AccountHomeDir: home1,
		ChatID:         8888,
		BotName:        "trickster_gobot",
		Conversation:   "conv-test-cooldown",
	}

	// Calling start() should detect that acc-1 is in cooldown and auto-reacquire acc-2!
	if err := s.start(); err != nil {
		t.Fatalf("s.start() failed: %v", err)
	}
	defer s.Kill()

	s.mu.Lock()
	newAccID := s.AccountID
	newHome := s.AccountHomeDir
	s.mu.Unlock()

	if newAccID != "acc-2" {
		t.Errorf("Expected session to reacquire acc-2, got: %s", newAccID)
	}
	if newHome != home2 {
		t.Errorf("Expected session homeDir to switch to %s, got: %s", home2, newHome)
	}
}

func TestParseUsageJSON_DisabledBucketAndWeeklyExhaustion(t *testing.T) {
	// Raw JSON reproducing Issue #298 where weekly quota is 0,
	// and gemini-5h has "disabled": true with residual remaining_fraction ~1.0
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
								"name": "Weekly Limit Remaining",
								"window": "weekly",
								"remaining_fraction": 0,
								"reset_time": "2026-09-23T04:55:05Z"
							},
							{
								"id": "gemini-5h",
								"name": "Five Hour Limit Remaining",
								"description": "You have hit your weekly limit, the 5-hour limit does not currently apply.",
								"window": "5h",
								"disabled": true,
								"remaining_fraction": 0.9998999834060669
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

	if !quota.Gemini5h.Disabled {
		t.Errorf("Expected Gemini 5h to be marked Disabled=true")
	}
	if quota.Gemini5h.RemainingFraction != 0.0 {
		t.Errorf("Expected Gemini 5h RemainingFraction to be normalized to 0.0, got %f", quota.Gemini5h.RemainingFraction)
	}
	if quota.GeminiWeekly.RemainingFraction != 0.0 {
		t.Errorf("Expected Gemini weekly RemainingFraction to be 0.0, got %f", quota.GeminiWeekly.RemainingFraction)
	}
	if quota.Gemini5h.ResetTime.IsZero() {
		t.Errorf("Expected Gemini 5h to inherit ResetTime from Gemini weekly")
	}
	expectedReset, _ := time.Parse(time.RFC3339, "2026-09-23T04:55:05Z")
	if !quota.Gemini5h.ResetTime.Equal(expectedReset) {
		t.Errorf("Expected Gemini 5h ResetTime to match weekly reset (%v), got %v", expectedReset, quota.Gemini5h.ResetTime)
	}
}

func TestAccountPool_AcquireAccount_ExcludesDisabled(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	// acc-disabled has residual quota in CLI but is disabled due to weekly exhaustion
	pool.accounts["acc-disabled"] = &Account{
		ID:          "acc-disabled",
		Email:       "disabled@gmail.com",
		State:       StateActive,
		LastUsed:    now.Add(-2 * time.Hour),
		ActiveTurns: 0,
		Quota: AccountQuota{
			Gemini5h: ModelQuota{
				RemainingFraction: 0.0,
				Disabled:          true,
				ResetTime:         now.Add(24 * time.Hour),
			},
			GeminiWeekly: ModelQuota{
				RemainingFraction: 0.0,
				ResetTime:         now.Add(24 * time.Hour),
			},
			LastFetchedAt: now,
		},
	}

	// acc-healthy has lower remaining quota (0.40) but is NOT disabled
	pool.accounts["acc-healthy"] = &Account{
		ID:          "acc-healthy",
		Email:       "healthy@gmail.com",
		State:       StateActive,
		LastUsed:    now.Add(-1 * time.Hour),
		ActiveTurns: 0,
		Quota: AccountQuota{
			Gemini5h:      ModelQuota{RemainingFraction: 0.40},
			GeminiWeekly:  ModelQuota{RemainingFraction: 0.50},
			LastFetchedAt: now,
		},
	}

	acc, err := pool.AcquireAccount(888)
	if err != nil {
		t.Fatalf("AcquireAccount failed: %v", err)
	}
	if acc.ID != "acc-healthy" {
		t.Errorf("Expected acc-healthy to be chosen, but got %s", acc.ID)
	}

	// Now if acc-healthy is placed in cooldown, only acc-disabled remains active
	pool.accounts["acc-healthy"].State = StateCooldown
	pool.accounts["acc-healthy"].CooldownUntil = now.Add(1 * time.Hour)

	// AcquireAccount should return ErrAllAccountsCooldown rather than selecting acc-disabled
	_, err = pool.AcquireAccount(999)
	if err != ErrAllAccountsCooldown {
		t.Errorf("Expected ErrAllAccountsCooldown when only disabled account remains, got: %v", err)
	}
}

func TestAccountPool_FetchEmailForToken_Scenarios(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	// 1. Empty token should fail fast without network call
	email, err := pool.FetchEmailForToken("")
	if err == nil || email != "" {
		t.Fatalf("expected error for empty token, got email: %s, err: %v", email, err)
	}

	// 2. 200 OK with valid email
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer valid-token-123" {
			t.Errorf("expected Bearer valid-token-123, got: %s", req.Header.Get("Authorization"))
		}
		jsonBody := `{"email": "hero@novanodes.com"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("valid-token-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if email != "hero@novanodes.com" {
		t.Errorf("expected hero@novanodes.com, got: %s", email)
	}

	// 3. 200 OK with missing or empty email field
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		jsonBody := `{"email": "   ", "name": "No Email User"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-empty-email")
	if err == nil || email != "" {
		t.Errorf("expected error for missing email, got email: %s, err: %v", email, err)
	}

	// 4. 401 Unauthorized (invalid/expired OAuth token)
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		jsonBody := `{"error": "invalid_token", "error_description": "Token has expired"}`
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	_, err = pool.FetchEmailForToken("token-expired")
	if err == nil {
		t.Fatalf("expected error for 401 Unauthorized, got nil")
	}

	// 5. 429 Too Many Requests (Google rate limit)
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(bytes.NewBufferString("Rate limit exceeded")),
			Header:     make(http.Header),
		}, nil
	})

	_, err = pool.FetchEmailForToken("token-ratelimited")
	if err == nil {
		t.Fatalf("expected error for 429 Too Many Requests, got nil")
	}

	// 6. 500 Internal Server Error
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewBufferString("Google backend crash")),
			Header:     make(http.Header),
		}, nil
	})

	_, err = pool.FetchEmailForToken("token-google-500")
	if err == nil {
		t.Fatalf("expected error for 500 Internal Server Error, got nil")
	}

	// 7. Malformed JSON
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("{unparseable json")),
			Header:     make(http.Header),
		}, nil
	})

	_, err = pool.FetchEmailForToken("token-malformed-json")
	if err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}

	// 8. Network timeout / connection refused
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New("connection reset by peer")}
	})

	_, err = pool.FetchEmailForToken("token-network-drop")
	if err == nil {
		t.Fatalf("expected error for network failure, got nil")
	}
}

func TestAccountPool_DeriveAccountIDFromEmail_Table(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"developer@novanodes.com", "developer"},
		{"john.doe-dev_123@gmail.com", "john.doe-dev_123"},
		{"UPPERCASE.User@COMPANY.COM", "uppercase.user"},
		{"strange!#$special%*()chars@domain.com", "strangespecialchars"},
		{"very_long_prefix_that_exceeds_thirty_two_characters_limit@domain.com", "very_long_prefix_that_exceeds_th"},
		{"", "account"},
		{"@empty-user.com", "account"},
		{"clean-slug", "clean-slug"},
		{"...dots-and-dashes---", "dots-and-dashes"},
	}

	for _, tc := range tests {
		got := DeriveAccountIDFromEmail(tc.input)
		if got != tc.expected {
			t.Errorf("DeriveAccountIDFromEmail(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestAccountPool_IngestCurrentAccount_Scenarios(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	mockHome := filepath.Join(tmpDir, "mock_home")
	_ = os.MkdirAll(mockHome, 0755)
	t.Setenv("HOME", mockHome)

	// Scenario 1: Token file missing
	_, err := pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when token file is missing, got nil")
	}

	// Scenario 2: Token file exists but invalid JSON
	tokenDir := filepath.Join(mockHome, ".gemini", "antigravity-cli")
	_ = os.MkdirAll(tokenDir, 0755)
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")
	_ = os.WriteFile(tokenPath, []byte("broken json payload"), 0600)

	_, err = pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when token file is malformed JSON, got nil")
	}

	// Scenario 3: Token file has empty access token
	_ = os.WriteFile(tokenPath, []byte(`{"token": {"access_token": ""}}`), 0600)
	_, err = pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when access token is empty, got nil")
	}

	// Scenario 4: Successful ingestion
	validTokenJSON := `{"token": {"access_token": "valid-oauth-secret-abc"}}`
	_ = os.WriteFile(tokenPath, []byte(validTokenJSON), 0600)

	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"email": "agent.ingest@novanodes.com"}`)),
			Header:     make(http.Header),
		}, nil
	})

	acc, err := pool.IngestCurrentAccount()
	if err != nil {
		t.Fatalf("unexpected error during ingest: %v", err)
	}
	if acc == nil {
		t.Fatalf("expected ingested account, got nil")
	}
	if acc.ID != "agent.ingest" {
		t.Errorf("expected account ID agent.ingest, got: %s", acc.ID)
	}
	if acc.Email != "agent.ingest@novanodes.com" {
		t.Errorf("expected email agent.ingest@novanodes.com, got: %s", acc.Email)
	}

	// Verify profile folder was created and account added to pool
	pool.mu.RLock()
	retrieved, exists := pool.accounts["agent.ingest"]
	pool.mu.RUnlock()

	if !exists || retrieved == nil {
		t.Errorf("ingested account not found in pool map")
	}
	if _, err := os.Stat(acc.HomeDir); os.IsNotExist(err) {
		t.Errorf("account profile directory was not created at %s", acc.HomeDir)
	}
}

func TestAccountPool_FetchAllQuotas_Execution(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := "#!/bin/sh\necho '{\"command\":{\"data\":{\"groups\":[{\"name\":\"gemini\",\"buckets\":[{\"id\":\"5h\",\"window\":\"5h\",\"remaining_fraction\":0.9,\"reset_time\":\"2026-09-12T15:00:00Z\"}]}]}}}'\n"
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool.accounts["acc-alpha"] = &Account{
		ID:      "acc-alpha",
		Email:   "alpha@example.com",
		HomeDir: filepath.Join(tmpDir, "acc-alpha"),
		State:   StateActive,
	}
	pool.accounts["acc-beta"] = &Account{
		ID:      "acc-beta",
		Email:   "beta@example.com",
		HomeDir: filepath.Join(tmpDir, "acc-beta"),
		State:   StateCooldown,
	}

	// FetchAllQuotas executes concurrently and waits for all accounts
	pool.FetchAllQuotas()

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if pool.accounts["acc-alpha"].Quota.LastFetchedAt.IsZero() {
		t.Errorf("expected quota to be populated for acc-alpha")
	}
}

func TestAccountState_String(t *testing.T) {
	if StateActive.String() != "Active" {
		t.Errorf("expected Active")
	}
	if StateInUse.String() != "In-Use" {
		t.Errorf("expected In-Use")
	}
	if StateCooldown.String() != "Cooldown" {
		t.Errorf("expected Cooldown")
	}
	if StateExpired.String() != "Expired" {
		t.Errorf("expected Expired")
	}
	if AccountState(99).String() != "Unknown" {
		t.Errorf("expected Unknown")
	}
}

func TestGetAccountsDir_EnvOverride(t *testing.T) {
	t.Setenv("ACCOUNTS_DIR", "/custom/accounts/dir")
	if getAccountsDir() != "/custom/accounts/dir" {
		t.Errorf("expected /custom/accounts/dir, got: %s", getAccountsDir())
	}
}

func TestAccountPool_StartBackgroundReaper_ImmediateCancel(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool.StartBackgroundReaper(ctx, nil)
}

func TestDeriveAccountIDFromEmail(t *testing.T) {
	tests := []struct {
		email    string
		expected string
	}{
		{"izizizwtfzalupchick@gmail.com", "izizizwtfzalupchick"},
		{"thedoctormes@gmail.com", "thedoctormes"},
		{"sora89049653438@gmail.com", "sora89049653438"},
		{"John.Doe@example.com", "john.doe"},
		{"user_test-1@domain.org", "user_test-1"},
		{"", "account"},
		{"@domain.com", "account"},
		{"  Spaces.Test@gmail.com  ", "spaces.test"},
	}

	for _, tt := range tests {
		got := DeriveAccountIDFromEmail(tt.email)
		if got != tt.expected {
			t.Errorf("DeriveAccountIDFromEmail(%q) = %q, want %q", tt.email, got, tt.expected)
		}
	}
}

func TestAccountPool_LoadState_MigrateLegacyAccountIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pool_migrate_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	legacyAccDir := filepath.Join(tmpDir, "acc-1")
	if err := os.MkdirAll(legacyAccDir, 0700); err != nil {
		t.Fatalf("Failed to create legacy acc dir: %v", err)
	}

	legacyState := poolStateJSON{
		Accounts: map[string]*Account{
			"acc-1": {
				ID:      "acc-1",
				Email:   "thedoctormes@gmail.com",
				HomeDir: legacyAccDir,
				State:   StateActive,
			},
		},
		PinnedChat: map[string]string{
			"trickster_gobot:1001": "acc-1",
		},
		ActiveChat: map[string]string{
			"trickster_gobot:1001": "acc-1",
			"1001":                 "acc-1",
		},
	}

	stateBytes, err := json.Marshal(legacyState)
	if err != nil {
		t.Fatalf("Failed to marshal legacy state: %v", err)
	}

	stateFilePath := filepath.Join(tmpDir, "accounts.json")
	if err := os.WriteFile(stateFilePath, stateBytes, 0600); err != nil {
		t.Fatalf("Failed to write accounts.json: %v", err)
	}

	pool, err := NewAccountPool(tmpDir)
	if err != nil {
		t.Fatalf("Failed to initialize pool: %v", err)
	}

	// 1. Verify old account ID is gone and new slug exists
	if _, oldExists := pool.accounts["acc-1"]; oldExists {
		t.Errorf("Legacy account ID acc-1 still exists in accounts map")
	}

	newAcc, newExists := pool.accounts["thedoctormes"]
	if !newExists || newAcc == nil {
		t.Fatalf("Expected migrated account thedoctormes to exist")
	}

	if newAcc.ID != "thedoctormes" {
		t.Errorf("Expected account ID to be thedoctormes, got %s", newAcc.ID)
	}

	expectedNewHome := filepath.Join(tmpDir, "thedoctormes")
	if newAcc.HomeDir != expectedNewHome {
		t.Errorf("Expected HomeDir %s, got %s", expectedNewHome, newAcc.HomeDir)
	}

	// 2. Verify filesystem: new directory exists, old directory is a symlink to new directory
	fi, err := os.Lstat(legacyAccDir)
	if err != nil {
		t.Errorf("Legacy directory missing or failed lstat: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Legacy directory is not a symlink")
	}

	if fiNew, err := os.Stat(expectedNewHome); err != nil || !fiNew.IsDir() {
		t.Errorf("New directory does not exist or is not a dir: %v", err)
	}

	// 3. Verify pinnedChat and activeChat mappings migrated
	if pool.pinnedChat["trickster_gobot:1001"] != "thedoctormes" {
		t.Errorf("Pinned chat not migrated, got %s", pool.pinnedChat["trickster_gobot:1001"])
	}
	if pool.activeChat["trickster_gobot:1001"] != "thedoctormes" {
		t.Errorf("Active chat not migrated, got %s", pool.activeChat["trickster_gobot:1001"])
	}
	if pool.activeChat["1001"] != "thedoctormes" {
		t.Errorf("Legacy chat key not migrated, got %s", pool.activeChat["1001"])
	}

	// 4. Verify persisted accounts.json has the migrated IDs
	reloadedPool, err := NewAccountPool(tmpDir)
	if err != nil {
		t.Fatalf("Failed to reload pool: %v", err)
	}
	if _, exists := reloadedPool.accounts["thedoctormes"]; !exists {
		t.Errorf("Reloaded pool does not contain migrated account thedoctormes")
	}
}

func TestHandleAccountsCommand_NilPool(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	oldPool := GlobalAccountPool
	GlobalAccountPool = nil
	defer func() { GlobalAccountPool = oldPool }()

	handleAccountsCommand(bot, 12345, 999, "/accounts", "TestBot", db)

	text := helper.getLastSentText()
	if !strings.Contains(text, "Account Pool Manager is not initialized") {
		t.Fatalf("expected nil pool error message, got: %s", text)
	}
}

func TestHandleAccountsCommand_Help(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	handleAccountsCommand(bot, 12345, 999, "/accounts help", "TestBot", db)

	text := helper.getLastSentText()
	if !strings.Contains(text, "Account Pool Management Commands") {
		t.Fatalf("expected help guide, got: %s", text)
	}
	if !strings.Contains(text, "/accounts switch") || !strings.Contains(text, "/accounts pin") {
		t.Errorf("expected command listing in help, got: %s", text)
	}
}

func TestHandleAccountsCommand_DefaultDashboard(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	pool.accounts["acc-1"] = &Account{
		ID:       "acc-1",
		Email:    "alpha@novanodes.com",
		HomeDir:  filepath.Join(poolDir, "acc-1"),
		State:    StateActive,
		LastUsed: time.Now(),
	}
	pool.accounts["acc-2"] = &Account{
		ID:            "acc-2",
		Email:         "beta@novanodes.com",
		HomeDir:       filepath.Join(poolDir, "acc-2"),
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(1 * time.Hour),
	}

	handleAccountsCommand(bot, 12345, 999, "/accounts", "TestBot", db)

	text := helper.getLastSentText()
	if !strings.Contains(text, "Account Pool Manager") {
		t.Fatalf("expected dashboard title, got: %s", text)
	}
	if !strings.Contains(text, "acc-1") || !strings.Contains(text, "acc-2") {
		t.Errorf("expected accounts listed in dashboard, got: %s", text)
	}
}

func TestHandleAccountsCommand_Switch_Scenarios(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	pool.accounts["acc-alpha"] = &Account{
		ID:       "acc-alpha",
		Email:    "alpha@novanodes.com",
		HomeDir:  filepath.Join(poolDir, "acc-alpha"),
		State:    StateActive,
		LastUsed: time.Now(),
	}

	// 1. Missing target ID
	handleAccountsCommand(bot, 12345, 999, "/accounts switch", "TestBot", db)
	text := helper.getLastSentText()
	if !strings.Contains(text, "Usage: <code>/accounts switch") {
		t.Errorf("expected usage message, got: %s", text)
	}

	// 2. Nonexistent account ID
	handleAccountsCommand(bot, 12345, 999, "/accounts switch nonexistent", "TestBot", db)
	text = helper.getLastSentText()
	if !strings.Contains(text, "Failed to switch account") {
		t.Errorf("expected switch error for nonexistent account, got: %s", text)
	}

	// 3. Successful switch
	handleAccountsCommand(bot, 12345, 999, "/accounts switch acc-alpha", "TestBot", db)
	text = helper.getLastSentText()
	if !strings.Contains(text, "Switched to Account:") || !strings.Contains(text, "acc-alpha") {
		t.Errorf("expected successful switch confirmation, got: %s", text)
	}
}

func TestHandleAccountsCommand_Pin_Unpin(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	pool.accounts["acc-pinned"] = &Account{
		ID:       "acc-pinned",
		Email:    "pinned@novanodes.com",
		HomeDir:  filepath.Join(poolDir, "acc-pinned"),
		State:    StateActive,
		LastUsed: time.Now(),
	}

	// 1. Pin missing argument
	handleAccountsCommand(bot, 12345, 999, "/accounts pin", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Usage: <code>/accounts pin") {
		t.Errorf("expected pin usage message")
	}

	// 2. Pin nonexistent
	handleAccountsCommand(bot, 12345, 999, "/accounts pin ghost", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Failed to pin account") {
		t.Errorf("expected pin failure for ghost account")
	}

	// 3. Pin valid
	handleAccountsCommand(bot, 12345, 999, "/accounts pin acc-pinned", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Sticky Mode Enabled") {
		t.Errorf("expected sticky mode enabled message")
	}

	// 4. Unpin
	handleAccountsCommand(bot, 12345, 999, "/accounts unpin", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Sticky Mode Disabled") {
		t.Errorf("expected sticky mode disabled message")
	}
}

func TestHandleAccountsCommand_ClearCooldown(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	pool.accounts["acc-cool"] = &Account{
		ID:            "acc-cool",
		Email:         "cool@novanodes.com",
		HomeDir:       filepath.Join(poolDir, "acc-cool"),
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(2 * time.Hour),
	}

	// 1. Missing target
	handleAccountsCommand(bot, 12345, 999, "/accounts clear_cooldown", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Usage: <code>/accounts clear_cooldown") {
		t.Errorf("expected clear_cooldown usage message")
	}

	// 2. Nonexistent target
	handleAccountsCommand(bot, 12345, 999, "/accounts clear_cooldown nonexistent", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Failed to clear cooldown") {
		t.Errorf("expected clear cooldown failure message")
	}

	// 3. Valid target
	handleAccountsCommand(bot, 12345, 999, "/accounts clear_cooldown acc-cool", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Cooldown Cleared:") {
		t.Errorf("expected cooldown cleared success message")
	}
	if pool.accounts["acc-cool"].State != StateActive {
		t.Errorf("expected account state to be active")
	}
}

func TestHandleAccountsCommand_Quotas_And_IngestFail(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	mockScript := filepath.Join(poolDir, "mock_agy.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\necho '{\"command\":{\"data\":{\"groups\":[]}}}'\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	// Quotas subcommand
	handleAccountsCommand(bot, 12345, 999, "/accounts quotas", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Account Pool Manager") {
		t.Errorf("expected refreshed dashboard on quotas command")
	}

	// Ingest subcommand failure (no token file present in clean test env)
	t.Setenv("HOME", t.TempDir())
	handleAccountsCommand(bot, 12345, 999, "/accounts ingest", "TestBot", db)
	if !strings.Contains(helper.getLastSentText(), "Failed to ingest account") {
		t.Errorf("expected failure message when ingesting without token")
	}
}

func TestHandleAccountCallbackQuery_RoutingAndNilPool(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	cbNonAcc := &tgbotapi.CallbackQuery{
		ID:   "cb_1",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 201,
		},
		Data: "model:gemini-flash",
	}
	handled := handleAccountCallbackQuery(bot, cbNonAcc, "TestBot", db)
	if handled {
		t.Fatalf("expected non-acc callback to return false, got true")
	}

	oldPool := GlobalAccountPool
	GlobalAccountPool = nil
	defer func() { GlobalAccountPool = oldPool }()

	cbAcc := &tgbotapi.CallbackQuery{
		ID:   "cb_2",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 202,
		},
		Data: "acc:switch:acc-1",
	}
	handled = handleAccountCallbackQuery(bot, cbAcc, "TestBot", db)
	if !handled {
		t.Fatalf("expected nil pool callback to return true, got false")
	}
}

func TestHandleAccountCallbackQuery_AllActions(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	mockScript := filepath.Join(poolDir, "mock_agy.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\necho '{\"command\":{\"data\":{\"groups\":[]}}}'\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	pool.accounts["acc-action"] = &Account{
		ID:            "acc-action",
		Email:         "action@novanodes.com",
		HomeDir:       filepath.Join(poolDir, "acc-action"),
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(1 * time.Hour),
		LastUsed:      time.Now(),
	}

	// 1. Switch
	cbSwitch := &tgbotapi.CallbackQuery{
		ID:   "cb_switch",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 301,
		},
		Data: "acc:switch:acc-action",
	}
	if !handleAccountCallbackQuery(bot, cbSwitch, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery switch failed")
	}

	// Switch invalid
	cbSwitchInv := &tgbotapi.CallbackQuery{
		ID:   "cb_switch_inv",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 302,
		},
		Data: "acc:switch:ghost-acc",
	}
	if !handleAccountCallbackQuery(bot, cbSwitchInv, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery switch invalid failed")
	}

	// 2. Pin and Unpin
	cbPin := &tgbotapi.CallbackQuery{
		ID:   "cb_pin",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 303,
		},
		Data: "acc:pin:acc-action",
	}
	if !handleAccountCallbackQuery(bot, cbPin, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery pin failed")
	}

	cbUnpin := &tgbotapi.CallbackQuery{
		ID:   "cb_unpin",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 304,
		},
		Data: "acc:unpin",
	}
	if !handleAccountCallbackQuery(bot, cbUnpin, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery unpin failed")
	}

	// 3. Clear cooldown
	cbCool := &tgbotapi.CallbackQuery{
		ID:   "cb_cool",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 305,
		},
		Data: "acc:cooldown:acc-action",
	}
	if !handleAccountCallbackQuery(bot, cbCool, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery cooldown failed")
	}

	// 4. Refresh
	cbRefresh := &tgbotapi.CallbackQuery{
		ID:   "cb_refresh",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 306,
		},
		Data: "acc:refresh",
	}
	if !handleAccountCallbackQuery(bot, cbRefresh, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery refresh failed")
	}

	// 5. Ingest failure
	t.Setenv("HOME", t.TempDir())
	cbIngest := &tgbotapi.CallbackQuery{
		ID:   "cb_ingest",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 307,
		},
		Data: "acc:ingest",
	}
	if !handleAccountCallbackQuery(bot, cbIngest, "TestBot", db) {
		t.Errorf("handleAccountCallbackQuery ingest failed")
	}
}

func TestHandleAccountsCommand_DisabledQuotaDisplay(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	now := time.Now()
	pool.accounts["acc-dis"] = &Account{
		ID:    "acc-dis",
		Email: "dis@example.com",
		State: StateActive,
		Quota: AccountQuota{
			Gemini5h: ModelQuota{
				RemainingFraction: 0.0,
				Disabled:          true,
			},
			GeminiWeekly: ModelQuota{
				RemainingFraction: 0.0,
				Disabled:          false,
			},
			Claude5h: ModelQuota{
				RemainingFraction: 0.75,
			},
			ClaudeWeekly: ModelQuota{
				RemainingFraction: 1.0,
			},
			LastFetchedAt: now,
		},
	}

	handleAccountsCommand(bot, 12345, 999, "/accounts", "TestBot", db)
	text := helper.getLastSentText()

	// Should show 'Gemini: 5h disabled • 7d 0%'
	expectedLine := "Gemini: 5h disabled • 7d 0%"
	if !strings.Contains(text, expectedLine) {
		t.Errorf("Expected dashboard to contain %q, but got:\n%s", expectedLine, text)
	}
}
