package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"(untitled session)", ""},
		{"untitled session", ""},
		{"(empty)", ""},
		{"Session active", ""},
		{"Fix Auth Bug", "Fix_Auth_Bug"},
		{"Рефакторинг кода и архитектуры", "Рефакторинг_кода_и_архитектуры"},
		{"Очень длинное название задачи которое обязательно обрежется по лимиту тридцати рун", "Очень_длинное_название_задачи"},
		{"Special #$% Symbols & More!", "Special_Symbols_More"},
		{"___Leading_Trailing___", "Leading_Trailing"},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatToolCalls(t *testing.T) {
	// 1. Empty calls
	if got := formatToolCalls(nil); got != "" {
		t.Errorf("Expected empty string for nil tool calls, got %q", got)
	}

	// 2. Summary present
	tcs1 := []interface{}{
		map[string]interface{}{
			"name":        "view_file",
			"toolSummary": "Inspect main configuration",
		},
	}
	got1 := formatToolCalls(tcs1)
	if !strings.Contains(got1, "🛠️ *Tool:* `view_file` — Inspect main configuration") {
		t.Errorf("Unexpected format with summary: %s", got1)
	}

	// 3. Action fallback
	tcs2 := []interface{}{
		map[string]interface{}{
			"name":       "run_command",
			"toolAction": "Running go test",
		},
	}
	got2 := formatToolCalls(tcs2)
	if !strings.Contains(got2, "🛠️ *Tool:* `run_command` — Running go test") {
		t.Errorf("Unexpected format with action: %s", got2)
	}

	// 4. Args fallback
	tcs3 := []interface{}{
		map[string]interface{}{
			"name": "list_dir",
			"args": map[string]interface{}{"path": "/root"},
		},
	}
	got3 := formatToolCalls(tcs3)
	if !strings.Contains(got3, "🛠️ *Tool:* `list_dir` —") {
		t.Errorf("Unexpected format with args: %s", got3)
	}

	// 5. Huge argumentsJson truncated
	hugeJSON := fmt.Sprintf(`{"code":"%s"}`, strings.Repeat("A", 200))
	tcs4 := []interface{}{
		map[string]interface{}{
			"name":          "write_to_file",
			"argumentsJson": hugeJSON,
		},
	}
	got4 := formatToolCalls(tcs4)
	if !strings.Contains(got4, "...") || len(got4) > 150 {
		t.Errorf("Expected truncation for huge argumentsJson, got length %d: %s", len(got4), got4)
	}
}

func TestHandleExportCommand_CleanFormattingAndFilename(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "export-clean-12345"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)

	// Set session title
	os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Refactor Engine Logic"), 0644)

	// Create transcript with mixed events
	fullJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nImplement clean export\n</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Starting implementation...","tool_calls":[{"name":"replace_file_content","toolSummary":"Apply clean export format"}]}
{"step":3,"created_at":"2026-09-05T10:00:10Z","source":"MODEL","type":"GENERIC","content":"MASSIVE_RAW_TOOL_DUMP_OUTPUT_SHOULD_BE_FILTERED"}
{"step":4,"created_at":"2026-09-05T10:00:15Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Export refactoring completed successfully."}
`
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(fullJSONL), 0644)

	user := User{
		ID:        999,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 12345, 999, "TestBot", user)

	// Verify export file was generated with expected title slug
	expectedFilename := fmt.Sprintf("session_Refactor_Engine_Logic_%s.md", safePrefix(sessionID, 8))
	exportPath := filepath.Join(mockAgents, "TestBot", "scratch", "exports", expectedFilename)

	contentBytes, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("Failed to read generated export file at %s: %v", exportPath, err)
	}

	content := string(contentBytes)
	if strings.Contains(content, "MASSIVE_RAW_TOOL_DUMP_OUTPUT_SHOULD_BE_FILTERED") {
		t.Errorf("Export file contained raw GENERIC tool output dump!")
	}

	if !strings.Contains(content, "🛠️ *Tool:* `replace_file_content` — Apply clean export format") {
		t.Errorf("Export file missing cleanly formatted tool call, got: %s", content)
	}

	if !strings.Contains(content, "Implement clean export") || !strings.Contains(content, "Export refactoring completed successfully.") {
		t.Errorf("Export file missing user request or assistant response, got: %s", content)
	}
}

func TestDownloadTelegramMedia_SessionExportAutoPrompt(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("# Session Export Markdown"))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"fileExport1","file_path":"%s"}}`, fileServer.URL+"/session_export.md")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	// 1. Session export with empty caption should trigger auto-prompt
	formatted, isFile, _, err := downloadTelegramMedia(bot, 12345, "fileExport1", ".md", "", "", "TestBot", "session_Fix_Auth_123.md")
	if err != nil || !isFile {
		t.Fatalf("Download failed: %v, isFile: %v", err, isFile)
	}
	if !strings.Contains(formatted, "Previous session context loaded from export file") {
		t.Errorf("Expected auto-prompt for session export file, got: %s", formatted)
	}

	// 2. Session export with user-supplied caption should keep user caption
	formatted2, _, _, err := downloadTelegramMedia(bot, 12345, "fileExport1", ".md", "", "User custom instructions", "TestBot", "session_Fix_Auth_123.md")
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if !strings.Contains(formatted2, "User custom instructions") {
		t.Errorf("Expected user caption to be preserved, got: %s", formatted2)
	}
	if strings.Contains(formatted2, "Previous session context loaded") {
		t.Errorf("Auto-prompt should NOT override user-supplied caption, got: %s", formatted2)
	}
}
