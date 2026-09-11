package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLI_Truncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exact10len", 10, "exact10len"},
		{"very long string to truncate", 10, "very lo..."},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, expected %q", tt.input, tt.maxLen, got, tt.expected)
		}
	}
}

func TestCLI_HandleScan_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()
	emptyAccounts := filepath.Join(tempDir, "empty_accounts")
	emptyBrain := filepath.Join(tempDir, "empty_brain")
	_ = os.MkdirAll(emptyAccounts, 0755)
	_ = os.MkdirAll(emptyBrain, 0755)

	// Should not panic or exit with error
	handleScan([]string{
		"--accounts-dir=" + emptyAccounts,
		"--brain-dir=" + emptyBrain,
	})

	handleScan([]string{
		"--accounts-dir=" + emptyAccounts,
		"--brain-dir=" + emptyBrain,
		"--json",
	})
}

func TestCLI_HandleDoctor_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()
	emptyAccounts := filepath.Join(tempDir, "empty_accounts")
	emptyBrain := filepath.Join(tempDir, "empty_brain")
	_ = os.MkdirAll(emptyAccounts, 0755)
	_ = os.MkdirAll(emptyBrain, 0755)

	handleDoctor([]string{
		"--accounts-dir=" + emptyAccounts,
		"--brain-dir=" + emptyBrain,
		"--max-age=24h",
	})

	handleDoctor([]string{
		"--accounts-dir=" + emptyAccounts,
		"--brain-dir=" + emptyBrain,
		"--json",
	})
}
