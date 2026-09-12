package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLI_PrintHelp(t *testing.T) {
	// Should execute and print help text without panicking
	printHelp()
}

func TestCLI_Truncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exact_len", 9, "exact_len"},
		{"longer_than_limit", 10, "longer_..."},
		{"   padded   ", 10, "padded"},
		{"", 5, ""},
	}

	for _, tc := range tests {
		got := truncate(tc.input, tc.maxLen)
		if got != tc.expected {
			t.Errorf("truncate(%q, %d) = %q, expected %q", tc.input, tc.maxLen, got, tc.expected)
		}
	}
}

func TestCLI_HandleScan_Scenarios(t *testing.T) {
	tmpDir := t.TempDir()
	accountsDir := filepath.Join(tmpDir, "accounts")
	brainDir := filepath.Join(tmpDir, "brain")
	_ = os.MkdirAll(accountsDir, 0755)
	_ = os.MkdirAll(brainDir, 0755)

	// 1. JSON mode
	handleScan([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--json",
	})

	// 2. Tabular mode (empty inventory)
	handleScan([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
	})

	// 3. Populated inventory
	sessDir := filepath.Join(brainDir, "sess-pop-1")
	_ = os.MkdirAll(sessDir, 0755)
	artFile := filepath.Join(sessDir, "architecture_spec.md")
	_ = os.WriteFile(artFile, []byte("# Core Architecture Spec\nComprehensive system design.\n"), 0644)

	handleScan([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
	})
	handleScan([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--json",
	})
}

func TestCLI_HandleDoctor_Scenarios(t *testing.T) {
	tmpDir := t.TempDir()
	accountsDir := filepath.Join(tmpDir, "accounts")
	brainDir := filepath.Join(tmpDir, "brain")
	_ = os.MkdirAll(accountsDir, 0755)
	_ = os.MkdirAll(brainDir, 0755)

	// 1. JSON mode (healthy)
	handleDoctor([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--max-age=24h",
		"--json",
	})

	// 2. Tabular mode (healthy / no orphans)
	handleDoctor([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--max-age=24h",
	})

	// 3. Populated orphan detection (modified 72 hours ago)
	sessDir := filepath.Join(brainDir, "sess-orphan-1")
	_ = os.MkdirAll(sessDir, 0755)
	artFile := filepath.Join(sessDir, "adr_protocol.md")
	_ = os.WriteFile(artFile, []byte("# ADR: Protocol Selection\nDesign decision.\n"), 0644)
	oldTime := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(artFile, oldTime, oldTime)

	handleDoctor([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--max-age=24h",
	})
	handleDoctor([]string{
		"--accounts-dir=" + accountsDir,
		"--brain-dir=" + brainDir,
		"--max-age=24h",
		"--json",
	})
}
