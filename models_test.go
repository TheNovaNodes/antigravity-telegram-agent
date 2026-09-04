package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetEmojiForModel(t *testing.T) {
	tests := []struct {
		modelID  string
		expected string
	}{
		{"gemini-3.7-flash-high", "⚡"},
		{"gemini-3.8-flash-high", "⚡"},
		{"gemini-3.1-pro-high", "🧠"},
		{"gemini-3.1-pro-low", "🧠"},
		{"claude-sonnet-4-6", "🟣"},
		{"claude-opus-4-6", "🟣"},
		{"gpt-oss-120b", "🟢"},
		{"llama-3-70b", "🟢"},
		{"custom-gpt-4o", "🤖"},
	}

	for _, tc := range tests {
		got := getEmojiForModel(tc.modelID)
		if got != tc.expected {
			t.Errorf("getEmojiForModel(%q) = %q; want %q", tc.modelID, got, tc.expected)
		}
	}
}

func TestFetchModels(t *testing.T) {
	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := `#!/bin/sh
echo "gemini-3.8-flash-high Google Gemini 3.8 Flash High"
echo "claude-sonnet-4-6 Anthropic Claude 3.7 Sonnet"
echo ""
`
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to create mock agy script: %v", err)
	}

	os.Setenv("AGY_BINARY", mockAgy)
	defer os.Unsetenv("AGY_BINARY")

	fetchModels()

	modelsMu.Lock()
	models := availableModels
	modelsMu.Unlock()

	if len(models) < 2 {
		t.Fatalf("Expected at least 2 models, got %d", len(models))
	}

	if models[0].ID != "gemini-3.8-flash-high" || models[0].Emoji != "⚡" {
		t.Errorf("Unexpected model 0: %+v", models[0])
	}

	if models[1].ID != "claude-sonnet-4-6" || models[1].Emoji != "🟣" {
		t.Errorf("Unexpected model 1: %+v", models[1])
	}
}

func TestFetchModels_InvalidBinary(t *testing.T) {
	os.Setenv("AGY_BINARY", "/nonexistent/path/agy")
	defer os.Unsetenv("AGY_BINARY")

	// Should not crash, simply logs error
	fetchModels()
}

func TestGetFallbackModel(t *testing.T) {
	tests := []struct {
		current  string
		expected string
	}{
		{"gemini-3.8-flash-high", "gemini-3.7-flash-high"},
		{"gemini-3.7-flash-high", "gemini-3.1-pro-high"},
		{"gemini-3.1-pro-high", "gemini-3.6-flash-low"},
		{"gemini-3.6-flash-low", ""},
		{"unknown-model", "gemini-3.7-flash-high"},
	}

	for _, tc := range tests {
		got := getFallbackModel(tc.current)
		if got != tc.expected {
			t.Errorf("getFallbackModel(%q) = %q; want %q", tc.current, got, tc.expected)
		}
	}
}
