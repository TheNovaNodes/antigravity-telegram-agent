package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
