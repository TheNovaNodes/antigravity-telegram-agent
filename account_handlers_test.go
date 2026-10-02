package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestAccountsDashboard_MasterDetailGridRendering(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	chatID := int64(98765)

	pool.accounts["acc-1"] = &Account{
		ID:      "acc-1",
		Email:   "acc1@novanodes.com",
		HomeDir: filepath.Join(tmpDir, "acc-1"),
		State:   StateActive,
	}
	pool.accounts["acc-2"] = &Account{
		ID:      "acc-2",
		Email:   "acc2@novanodes.com",
		HomeDir: filepath.Join(tmpDir, "acc-2"),
		State:   StateInUse,
	}
	pool.accounts["acc-3"] = &Account{
		ID:            "acc-3",
		Email:         "acc3@novanodes.com",
		HomeDir:       filepath.Join(tmpDir, "acc-3"),
		State:         StateCooldown,
		CooldownUntil: time.Now().Add(30 * time.Minute),
	}
	pool.accounts["acc-4"] = &Account{
		ID:      "acc-4",
		Email:   "acc4@novanodes.com",
		HomeDir: filepath.Join(tmpDir, "acc-4"),
		State:   StateFrozen,
	}
	pool.accounts["acc-5"] = &Account{
		ID:      "acc-5",
		Email:   "acc5@novanodes.com",
		HomeDir: filepath.Join(tmpDir, "acc-5"),
		State:   StateExpired,
	}

	// Pin chat to active acc-1
	if err := pool.PinAccount(chatID, "acc-1", "TestBot"); err != nil {
		t.Fatalf("Failed to pin acc-1: %v", err)
	}
	// Set active account to acc-2 for unpinned chat representation
	pool.activeChat["TestBot:98765"] = "acc-2"

	dashText, keyboard := formatAccountsDashboard(pool, chatID, "TestBot")

	if !strings.Contains(dashText, "Account Pool Manager") {
		t.Fatalf("Expected dashboard title in dashText, got:\n%s", dashText)
	}

	// Verify all accounts are mentioned in overview text
	for _, id := range []string{"acc-1", "acc-2", "acc-3", "acc-4", "acc-5"} {
		if !strings.Contains(dashText, fmt.Sprintf("[%s]", id)) {
			t.Errorf("Expected account %s in dashboard text", id)
		}
	}

	// Verify grid layout of account buttons: 5 accounts -> 3 rows (2 + 2 + 1)
	rows := keyboard.InlineKeyboard
	if len(rows) < 3 {
		t.Fatalf("Expected at least 3 rows in keyboard, got %d", len(rows))
	}

	// Check row 0 (2 buttons)
	if len(rows[0]) != 2 {
		t.Errorf("Expected 2 buttons in row 0, got %d", len(rows[0]))
	}
	// Check row 1 (2 buttons)
	if len(rows[1]) != 2 {
		t.Errorf("Expected 2 buttons in row 1, got %d", len(rows[1]))
	}
	// Check row 2 (1 button)
	if len(rows[2]) != 1 {
		t.Errorf("Expected 1 button in row 2, got %d", len(rows[2]))
	}

	// Flatten account buttons and verify callback data and badges
	var allAccButtons []tgbotapi.InlineKeyboardButton
	for i := 0; i < 3; i++ {
		allAccButtons = append(allAccButtons, rows[i]...)
	}

	if len(allAccButtons) != 5 {
		t.Fatalf("Expected 5 account buttons, got %d", len(allAccButtons))
	}

	btnByAccount := make(map[string]tgbotapi.InlineKeyboardButton)
	for _, btn := range allAccButtons {
		if !strings.HasPrefix(*btn.CallbackData, "acc:manage:") {
			t.Errorf("Expected acc:manage prefix in callback data, got %s", *btn.CallbackData)
		}
		accID := strings.TrimPrefix(*btn.CallbackData, "acc:manage:")
		btnByAccount[accID] = btn
	}

	// Check badges and indicators
	// acc-1 is Active and pinned: should have 🟢 and 🔒
	if btn, ok := btnByAccount["acc-1"]; ok {
		if !strings.Contains(btn.Text, "🟢") || !strings.Contains(btn.Text, "🔒") {
			t.Errorf("acc-1 button text expected 🟢 and 🔒, got %s", btn.Text)
		}
	} else {
		t.Errorf("acc-1 button missing")
	}

	// acc-2 is InUse: should have ⚡
	if btn, ok := btnByAccount["acc-2"]; ok {
		if !strings.Contains(btn.Text, "⚡") {
			t.Errorf("acc-2 button text expected ⚡, got %s", btn.Text)
		}
	} else {
		t.Errorf("acc-2 button missing")
	}

	// acc-3 is Cooldown: should have ⏳
	if btn, ok := btnByAccount["acc-3"]; ok {
		if !strings.Contains(btn.Text, "⏳") {
			t.Errorf("acc-3 button text expected ⏳, got %s", btn.Text)
		}
	} else {
		t.Errorf("acc-3 button missing")
	}

	// acc-4 is Frozen: should have 🧊
	if btn, ok := btnByAccount["acc-4"]; ok {
		if !strings.Contains(btn.Text, "🧊") {
			t.Errorf("acc-4 button text expected 🧊, got %s", btn.Text)
		}
	} else {
		t.Errorf("acc-4 button missing")
	}

	// acc-5 is Expired: should have 🔴
	if btn, ok := btnByAccount["acc-5"]; ok {
		if !strings.Contains(btn.Text, "🔴") {
			t.Errorf("acc-5 button text expected 🔴, got %s", btn.Text)
		}
	} else {
		t.Errorf("acc-5 button missing")
	}

	// Check unpin row exists since chat is pinned
	unpinFound := false
	for _, row := range rows {
		for _, btn := range row {
			if *btn.CallbackData == "acc:unpin" {
				unpinFound = true
			}
		}
	}
	if !unpinFound {
		t.Errorf("Expected unpin button in keyboard when chat is pinned")
	}

	// Check action row exists
	refreshFound := false
	ingestFound := false
	for _, row := range rows {
		for _, btn := range row {
			if *btn.CallbackData == "acc:refresh" {
				refreshFound = true
			}
			if *btn.CallbackData == "acc:ingest" {
				ingestFound = true
			}
		}
	}
	if !refreshFound || !ingestFound {
		t.Errorf("Expected refresh and ingest buttons in keyboard")
	}
}

func TestAccountCard_ButtonsAndQuotaFormatting(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	chatID := int64(112233)
	now := time.Now()

	acc := &Account{
		ID:          "lead-acc",
		Email:       "lead@novanodes.com",
		HomeDir:     filepath.Join(tmpDir, "lead-acc"),
		State:       StateActive,
		ActiveTurns: 42,
		TotalErrors: 1,
		LastUsed:    now,
		Quota: AccountQuota{
			LastFetchedAt: now,
			Gemini5h: ModelQuota{
				RemainingFraction: 0.85,
				ResetTime:         now.Add(2 * time.Hour),
			},
			GeminiWeekly: ModelQuota{
				RemainingFraction: 0.50,
			},
			Claude5h: ModelQuota{
				RemainingFraction: 0.90,
			},
			ClaudeWeekly: ModelQuota{
				RemainingFraction: 0.75,
			},
		},
	}
	pool.accounts["lead-acc"] = acc

	// Scenario A: Standard active, not current, not pinned
	cardText, keyboard := formatAccountCard(pool, acc, chatID, "TestBot")

	if !strings.Contains(cardText, "Manage Account:") || !strings.Contains(cardText, "lead-acc") {
		t.Fatalf("Expected card header, got:\n%s", cardText)
	}
	if !strings.Contains(cardText, "lead@novanodes.com") {
		t.Errorf("Expected email in card text")
	}
	if !strings.Contains(cardText, "Active (Ready)") {
		t.Errorf("Expected Active (Ready) in card text")
	}
	if !strings.Contains(cardText, "85%") || !strings.Contains(cardText, "50%") || !strings.Contains(cardText, "90%") {
		t.Errorf("Expected quota percentages in card text, got:\n%s", cardText)
	}
	if !strings.Contains(cardText, "Active Turns: 42") || !strings.Contains(cardText, "Total Errors: 1") {
		t.Errorf("Expected stats in card text")
	}

	// Verify full-width buttons (1 per row)
	for i, row := range keyboard.InlineKeyboard {
		if len(row) != 1 {
			t.Errorf("Expected row %d to have exactly 1 button (full-width), got %d", i, len(row))
		}
	}

	// Verify action buttons present
	btnCallbacks := make(map[string]string)
	for _, row := range keyboard.InlineKeyboard {
		btn := row[0]
		btnCallbacks[*btn.CallbackData] = btn.Text
	}

	if _, ok := btnCallbacks["acc:switch:lead-acc"]; !ok {
		t.Errorf("Expected Switch button")
	}
	if _, ok := btnCallbacks["acc:pin:lead-acc"]; !ok {
		t.Errorf("Expected Pin button")
	}
	if _, ok := btnCallbacks["acc:freeze:lead-acc"]; !ok {
		t.Errorf("Expected Freeze button")
	}
	if _, ok := btnCallbacks["acc:del_confirm:lead-acc"]; !ok {
		t.Errorf("Expected Delete button")
	}
	if _, ok := btnCallbacks["acc:back"]; !ok {
		t.Errorf("Expected Back button")
	}

	// Scenario B: Frozen account
	acc.State = StateFrozen
	_, kbFrozen := formatAccountCard(pool, acc, chatID, "TestBot")
	frozenCallbacks := make(map[string]string)
	for _, row := range kbFrozen.InlineKeyboard {
		btn := row[0]
		frozenCallbacks[*btn.CallbackData] = btn.Text
	}
	if _, ok := frozenCallbacks["acc:switch:lead-acc"]; ok {
		t.Errorf("Frozen account should NOT have Switch button")
	}
	if text, ok := frozenCallbacks["acc:unfreeze:lead-acc"]; !ok || !strings.Contains(text, "Unfreeze") {
		t.Errorf("Expected Unfreeze button for frozen account")
	}

	// Scenario C: Cooldown account
	acc.State = StateCooldown
	acc.CooldownUntil = time.Now().Add(10 * time.Minute)
	_, kbCooldown := formatAccountCard(pool, acc, chatID, "TestBot")
	coolCallbacks := make(map[string]string)
	for _, row := range kbCooldown.InlineKeyboard {
		btn := row[0]
		coolCallbacks[*btn.CallbackData] = btn.Text
	}
	if _, ok := coolCallbacks["acc:switch:lead-acc"]; ok {
		t.Errorf("Cooldown account should NOT have Switch button")
	}
	if text, ok := coolCallbacks["acc:cooldown:lead-acc"]; !ok || !strings.Contains(text, "Reset Cooldown") {
		t.Errorf("Expected Reset Cooldown button for cooldown account")
	}

	// Scenario D: Pinned account
	acc.State = StateActive
	_ = pool.PinAccount(chatID, "lead-acc", "TestBot")
	cardTextPinned, kbPinned := formatAccountCard(pool, acc, chatID, "TestBot")
	if !strings.Contains(cardTextPinned, "PINNED 🔒") {
		t.Errorf("Expected PINNED badge in card text for pinned account")
	}
	pinnedCallbacks := make(map[string]string)
	for _, row := range kbPinned.InlineKeyboard {
		btn := row[0]
		pinnedCallbacks[*btn.CallbackData] = btn.Text
	}
	if text, ok := pinnedCallbacks["acc:unpin:lead-acc"]; !ok || !strings.Contains(text, "Unpin") {
		t.Errorf("Expected Unpin button for pinned account")
	}
}

func TestAccountCallback_NavigationMasterDetailBack(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	h := filepath.Join(poolDir, "acc-nav")
	_ = os.MkdirAll(h, 0755)

	pool.accounts["acc-nav"] = &Account{
		ID:      "acc-nav",
		Email:   "nav@novanodes.com",
		HomeDir: h,
		State:   StateActive,
	}

	chatID := int64(12345)

	// Step 1: Click acc:manage:acc-nav (Master -> Detail)
	cbManage := &tgbotapi.CallbackQuery{
		ID:   "cb_manage",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 1001,
		},
		Data: "acc:manage:acc-nav",
	}
	if !handleAccountCallbackQuery(bot, cbManage, "TestBot", db) {
		t.Fatalf("acc:manage callback failed")
	}
	sent1 := helper.getLastSentText()
	if !strings.Contains(sent1, "Manage Account:") || !strings.Contains(sent1, "acc-nav") {
		t.Fatalf("Expected account card text after acc:manage, got:\n%s", sent1)
	}

	// Step 2: Click acc:back (Detail -> Master)
	cbBack := &tgbotapi.CallbackQuery{
		ID:   "cb_back",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 1001,
		},
		Data: "acc:back",
	}
	if !handleAccountCallbackQuery(bot, cbBack, "TestBot", db) {
		t.Fatalf("acc:back callback failed")
	}
	sent2 := helper.getLastSentText()
	if !strings.Contains(sent2, "Account Pool Manager") {
		t.Fatalf("Expected dashboard text after acc:back, got:\n%s", sent2)
	}

	// Step 3: Click acc:del_confirm:acc-nav (Detail -> Confirm Deletion dialog)
	cbDelConfirm := &tgbotapi.CallbackQuery{
		ID:   "cb_del_confirm",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 1001,
		},
		Data: "acc:del_confirm:acc-nav",
	}
	if !handleAccountCallbackQuery(bot, cbDelConfirm, "TestBot", db) {
		t.Fatalf("acc:del_confirm callback failed")
	}
	sent3 := helper.getLastSentText()
	if !strings.Contains(sent3, "Confirm Deletion of Account") {
		t.Fatalf("Expected confirm deletion dialog, got:\n%s", sent3)
	}

	// Step 4: Click acc:del_cancel:acc-nav (Cancel -> return to Detail Card)
	cbDelCancel := &tgbotapi.CallbackQuery{
		ID:   "cb_del_cancel",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 1001,
		},
		Data: "acc:del_cancel:acc-nav",
	}
	if !handleAccountCallbackQuery(bot, cbDelCancel, "TestBot", db) {
		t.Fatalf("acc:del_cancel callback failed")
	}
	sent4 := helper.getLastSentText()
	if !strings.Contains(sent4, "Manage Account:") || !strings.Contains(sent4, "acc-nav") {
		t.Fatalf("Expected return to account card after del_cancel, got:\n%s", sent4)
	}
}

func TestAccountCallback_ManageNonExistentAccountSafety(t *testing.T) {
	db, bot, helper := setupTestDBAndBot(t)
	defer helper.Close()
	defer db.Close()

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	chatID := int64(12345)

	// Callback to ghost account
	cbGhost := &tgbotapi.CallbackQuery{
		ID:   "cb_ghost",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 2001,
		},
		Data: "acc:manage:ghost-account-999",
	}

	// Must handle safely without panic
	if !handleAccountCallbackQuery(bot, cbGhost, "TestBot", db) {
		t.Fatalf("handleAccountCallbackQuery for ghost account should return true")
	}

	// Should refresh dashboard on missing account
	sent := helper.getLastSentText()
	if !strings.Contains(sent, "Account Pool Manager") {
		t.Fatalf("Expected dashboard refreshed for missing account, got:\n%s", sent)
	}

	// Test empty target ID safely
	cbEmpty := &tgbotapi.CallbackQuery{
		ID:   "cb_empty",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 2002,
		},
		Data: "acc:manage",
	}
	if !handleAccountCallbackQuery(bot, cbEmpty, "TestBot", db) {
		t.Fatalf("handleAccountCallbackQuery for empty manage should return true")
	}

	// Test del_cancel without targetID safely returns to dashboard
	cbCancelNoTarget := &tgbotapi.CallbackQuery{
		ID:   "cb_cancel_notarget",
		From: &tgbotapi.User{ID: 999},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: chatID},
			MessageID: 2003,
		},
		Data: "acc:del_cancel",
	}
	if !handleAccountCallbackQuery(bot, cbCancelNoTarget, "TestBot", db) {
		t.Fatalf("handleAccountCallbackQuery for cancel without target should return true")
	}
	sentCancel := helper.getLastSentText()
	if !strings.Contains(sentCancel, "Account Pool Manager") {
		t.Fatalf("Expected dashboard refreshed on cancel without target, got:\n%s", sentCancel)
	}
}
