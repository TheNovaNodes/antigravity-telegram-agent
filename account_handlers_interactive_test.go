package main

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func setupTestDBAndBot(t *testing.T) (*sql.DB, *tgbotapi.BotAPI, *httptestServerHelper) {
	t.Helper()
	ts, sent, mu := createStrictTelegramMockServer(t)

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("MOCK_TOKEN", ts.URL+"/bot%s/%s")
	if err != nil {
		ts.Close()
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)
	db := initDB("TestInteractiveBot")

	helper := &httptestServerHelper{
		server: ts,
		sent:   sent,
		mu:     mu,
	}
	return db, bot, helper
}

type httptestServerHelper struct {
	server interface{ Close() }
	sent   *[]string
	mu     interface {
		Lock()
		Unlock()
	}
}

func (h *httptestServerHelper) Close() {
	h.server.Close()
}

func (h *httptestServerHelper) getLastSentText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(*h.sent) == 0 {
		return ""
	}
	last := (*h.sent)[len(*h.sent)-1]
	vals, _ := url.ParseQuery(last)
	return vals.Get("text")
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
