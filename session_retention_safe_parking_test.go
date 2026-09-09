package main

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountPool_BotScopedIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	pool := &AccountPool{
		accountsDir: tmpDir,
		accounts:    make(map[string]*Account),
		pinnedChat:  make(map[string]string),
		activeChat:  make(map[string]string),
		client:      &http.Client{Timeout: 5 * time.Second},
	}

	pool.accounts["acc-1"] = &Account{
		ID:       "acc-1",
		Email:    "acc1@example.com",
		HomeDir:  filepath.Join(tmpDir, "acc-1"),
		State:    StateActive,
		LastUsed: time.Now().Add(-1 * time.Hour),
	}
	pool.accounts["acc-2"] = &Account{
		ID:       "acc-2",
		Email:    "acc2@example.com",
		HomeDir:  filepath.Join(tmpDir, "acc-2"),
		State:    StateActive,
		LastUsed: time.Now().Add(-2 * time.Hour),
	}

	chatID := int64(173681771)
	botA := "Caduceus_brobot"
	botB := "trickster_gobot"

	// Pin botA to acc-1
	if err := pool.PinAccount(chatID, "acc-1", botA); err != nil {
		t.Fatalf("PinAccount failed: %v", err)
	}

	// botA must be pinned to acc-1
	if !pool.IsPinned(chatID, botA) {
		t.Errorf("Expected botA (%s) to be pinned", botA)
	}
	if pinnedID, ok := pool.GetPinnedAccount(chatID, botA); !ok || pinnedID != "acc-1" {
		t.Errorf("Expected botA pinned to acc-1, got %s (ok=%v)", pinnedID, ok)
	}

	// botB must NOT be pinned
	if pool.IsPinned(chatID, botB) {
		t.Errorf("Expected botB (%s) NOT to be pinned", botB)
	}

	// Switch botB to acc-2
	if err := pool.SwitchAccount(chatID, "acc-2", botB); err != nil {
		t.Fatalf("SwitchAccount failed: %v", err)
	}

	// Verify isolated active assignments
	activeA := pool.GetActiveAccountForChat(chatID, botA)
	if activeA == nil || activeA.ID != "acc-1" {
		t.Errorf("Expected botA active account to be acc-1, got: %+v", activeA)
	}

	activeB := pool.GetActiveAccountForChat(chatID, botB)
	if activeB == nil || activeB.ID != "acc-2" {
		t.Errorf("Expected botB active account to be acc-2, got: %+v", activeB)
	}

	// Unpin botA
	if err := pool.UnpinAccount(chatID, botA); err != nil {
		t.Fatalf("UnpinAccount failed: %v", err)
	}
	if pool.IsPinned(chatID, botA) {
		t.Errorf("Expected botA to be unpinned")
	}
}

func TestResetChatSessionCache_PreserveDBSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(99991)
	chatID := int64(99991)
	botName := "Caduceus_brobot"
	convID := "conv-preserve-test-999"

	// Setup user in DB
	_, err := db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id, voice_reply) VALUES (?, '', 'gemini-3.8-flash-high', 0, ?, 0)",
		userID, convID)
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	// Evict in-memory cache but PRESERVE SQLite session
	resetChatSessionCache(db, botName, userID, chatID, true)

	var dbSessID sql.NullString
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if !dbSessID.Valid || dbSessID.String != convID {
		t.Errorf("Expected session_id to be preserved as %s, got: %+v", convID, dbSessID)
	}

	// Call default resetChatSessionCache (wiping DB)
	resetChatSessionCache(db, botName, userID, chatID)
	err = db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessID)
	if err != nil {
		t.Fatalf("Failed to query user: %v", err)
	}
	if dbSessID.Valid && dbSessID.String != "" {
		t.Errorf("Expected session_id to be NULL after destructive reset, got: %s", dbSessID.String)
	}
}

func TestEnsureSharedAccountDirectories_SymlinkCreation(t *testing.T) {
	sharedTmp := t.TempDir()
	expectedShared := filepath.Join(sharedTmp, "conversations")
	t.Setenv("CONVERSATIONS_DIR", expectedShared)
	tmpAccHome := t.TempDir()

	// Call EnsureSharedAccountDirectories
	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed: %v", err)
	}

	accConvs := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli", "conversations")
	fi, err := os.Lstat(accConvs)
	if err != nil {
		t.Fatalf("Failed to lstat %s: %v", accConvs, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Expected %s to be a symlink, got mode: %v", accConvs, fi.Mode())
	}

	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink %s: %v", accConvs, err)
	}
	if target != expectedShared {
		t.Errorf("Expected symlink target %s, got: %s", expectedShared, target)
	}
}
