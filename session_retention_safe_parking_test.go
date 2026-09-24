package main

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("SYSTEM_HOME", sharedTmp)
	tmpAccHome := t.TempDir()

	// Create a pre-existing dummy directory in .cache to verify duplicate cleanup
	preExistingCache := filepath.Join(tmpAccHome, ".cache", "dummy")
	if err := os.MkdirAll(preExistingCache, 0755); err != nil {
		t.Fatalf("Failed to create preExistingCache: %v", err)
	}

	// Call EnsureSharedAccountDirectories
	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed: %v", err)
	}

	// 1. Verify conversations symlink
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

	// 2. Verify .cache, go, and .npm symlinks
	checks := []struct {
		name     string
		path     string
		expected string
	}{
		{".cache", filepath.Join(tmpAccHome, ".cache"), filepath.Join(sharedTmp, ".cache")},
		{"go", filepath.Join(tmpAccHome, "go"), filepath.Join(sharedTmp, "go")},
		{".npm", filepath.Join(tmpAccHome, ".npm"), filepath.Join(sharedTmp, ".npm")},
	}

	for _, tc := range checks {
		info, lstatErr := os.Lstat(tc.path)
		if lstatErr != nil {
			t.Fatalf("Failed to lstat %s: %v", tc.path, lstatErr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("Expected %s to be a symlink, got mode: %v", tc.path, info.Mode())
		}
		linkTarget, readlinkErr := os.Readlink(tc.path)
		if readlinkErr != nil {
			t.Fatalf("Failed to readlink %s: %v", tc.path, readlinkErr)
		}
		if linkTarget != tc.expected {
			t.Errorf("[%s] Expected symlink target %s, got: %s", tc.name, tc.expected, linkTarget)
		}
	}
}

func TestSession_EnvSharedCachesInjection(t *testing.T) {
	sharedTmp := t.TempDir()
	t.Setenv("SYSTEM_HOME", sharedTmp)
	t.Setenv("SHARED_CACHE_DIR", filepath.Join(sharedTmp, "custom_cache"))
	t.Setenv("SHARED_GOPATH_DIR", filepath.Join(sharedTmp, "custom_go"))
	t.Setenv("SHARED_NPM_DIR", filepath.Join(sharedTmp, "custom_npm"))

	accHome := filepath.Join(sharedTmp, "acc_home")
	_ = os.MkdirAll(accHome, 0755)

	s := &AgySession{
		AccountHomeDir: accHome,
	}

	env := buildChildEnv(s.AccountHomeDir)

	expectedGo := filepath.Join(sharedTmp, "custom_go")
	expectedCache := filepath.Join(sharedTmp, "custom_cache", "go-build")
	expectedNpm := filepath.Join(sharedTmp, "custom_npm")
	expectedPip := filepath.Join(sharedTmp, "custom_cache", "pip")

	checkEnv := func(key, val string) {
		prefix := key + "="
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				found = true
				if e != prefix+val {
					t.Errorf("Expected %s%s, got %s", prefix, val, e)
				}
			}
		}
		if !found {
			t.Errorf("Variable %s not found in env", key)
		}
	}

	checkEnv("GOPATH", expectedGo)
	checkEnv("GOCACHE", expectedCache)
	checkEnv("NPM_CONFIG_CACHE", expectedNpm)
	checkEnv("PIP_CACHE_DIR", expectedPip)
	checkEnv("HOME", accHome)
}

func TestEnsureSharedAccountDirectories_DanglingSymlinkRecovery(t *testing.T) {
	sharedTmp := t.TempDir()
	expectedShared := filepath.Join(sharedTmp, "conversations")
	t.Setenv("CONVERSATIONS_DIR", expectedShared)
	t.Setenv("SYSTEM_HOME", sharedTmp)
	tmpAccHome := t.TempDir()

	accCliDir := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(accCliDir, 0755); err != nil {
		t.Fatalf("Failed to create accCliDir: %v", err)
	}
	accConvs := filepath.Join(accCliDir, "conversations")

	// 1. Create a pre-existing DANGLING symlink pointing to a non-existent directory
	nonExistentTarget := filepath.Join(sharedTmp, "non_existent_old_conversations")
	if err := os.Symlink(nonExistentTarget, accConvs); err != nil {
		t.Fatalf("Failed to create dummy dangling symlink: %v", err)
	}

	// Verify that the symlink is indeed dangling (os.Stat returns ErrNotExist, os.Lstat succeeds)
	if _, statErr := os.Stat(accConvs); !os.IsNotExist(statErr) {
		t.Fatalf("Expected os.Stat to return ErrNotExist for dangling symlink, got %v", statErr)
	}

	// 2. Call EnsureSharedAccountDirectories
	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed on dangling symlink: %v", err)
	}

	// 3. Verify that the dangling symlink was healed and now points to the existing shared conversations dir
	fi, err := os.Lstat(accConvs)
	if err != nil {
		t.Fatalf("Failed to lstat healed conversations symlink %s: %v", accConvs, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Expected %s to be a symlink, got mode: %v", accConvs, fi.Mode())
	}
	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink %s: %v", accConvs, err)
	}
	if target != expectedShared {
		t.Errorf("Expected healed symlink target %s, got: %s", expectedShared, target)
	}

	// 4. Assert that the symlink target is fully writable (mkdir / stat pass without EEXIST)
	if _, statErr := os.Stat(accConvs); statErr != nil {
		t.Errorf("Expected healed symlink target to exist on disk: %v", statErr)
	}
}

func TestEnsureSharedAccountDirectories_SystemHomeFallback(t *testing.T) {
	sharedTmp := t.TempDir()
	t.Setenv("CONVERSATIONS_DIR", "")
	t.Setenv("SYSTEM_HOME", sharedTmp)
	t.Setenv("HOME", "/etc/antigravity-bot/accounts/service_account")

	tmpAccHome := t.TempDir()

	if err := EnsureSharedAccountDirectories(tmpAccHome); err != nil {
		t.Fatalf("EnsureSharedAccountDirectories failed: %v", err)
	}

	accConvs := filepath.Join(tmpAccHome, ".gemini", "antigravity-cli", "conversations")
	target, err := os.Readlink(accConvs)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}

	expectedBase := filepath.Join(sharedTmp, ".gemini", "antigravity-cli", "conversations")
	if target != expectedBase {
		t.Errorf("Expected symlink target %s derived from SYSTEM_HOME, got: %s", expectedBase, target)
	}
}

func TestEnsureSymlink_DanglingSymlinkSelfHealing(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "real_target")
	symlinkPath := filepath.Join(tmpDir, "test_symlink")

	// Create dangling symlink pointing to targetDir before targetDir exists
	_ = os.Symlink(targetDir, symlinkPath)

	// ensureSymlink should detect targetDir didn't exist, create targetDir, and validate symlink
	if err := ensureSymlink(targetDir, symlinkPath); err != nil {
		t.Fatalf("ensureSymlink failed: %v", err)
	}

	if _, err := os.Stat(symlinkPath); err != nil {
		t.Errorf("Expected symlink target to exist after ensureSymlink: %v", err)
	}
}
