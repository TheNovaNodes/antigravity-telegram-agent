package main

import (
	"archive/zip"
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

func TestHandleExportCommand_WithArtifacts_PackagesZipAndEnrichesCaption(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "export-artifacts-7788"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create test logs dir: %v", err)
	}

	// Set session title
	_ = os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Architecture Refactoring"), 0644)

	// Create valid transcript
	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Refactor architecture</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Drafting ADR..."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)

	// Create engineering artifacts in sessionDir
	adrContent := "# Architecture Decision Record: Pure Go Engine\n\n## Status\nAccepted\n\n## Context\nMigrating to pure Go.\n"
	chkContent := "# Deployment Checklist\n\n- [ ] Run test suite\n- [ ] Verify SAST\n"
	_ = os.WriteFile(filepath.Join(sessionDir, "ADR_001_Pure_Go.md"), []byte(adrContent), 0644)
	_ = os.WriteFile(filepath.Join(sessionDir, "CHECKLIST_Deploy.md"), []byte(chkContent), 0644)

	user := User{
		ID:        888,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 554433, 888, "HarvesterBot", user)

	// 1. Verify transcript and zip bundle files generated on disk in scratch/exports
	expectedMdFilename := fmt.Sprintf("session_Architecture_Refactoring_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_Architecture_Refactoring_%s.zip", safePrefix(sessionID, 8))

	exportDir := filepath.Join(mockAgents, "HarvesterBot", "scratch", "exports")
	mdPath := filepath.Join(exportDir, expectedMdFilename)
	zipPath := filepath.Join(exportDir, expectedZipFilename)

	if _, err := os.Stat(mdPath); err != nil {
		t.Fatalf("Expected transcript markdown file at %s, got error: %v", mdPath, err)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("Expected artifacts zip bundle file at %s, got error: %v", zipPath, err)
	}

	// 2. Verify ZIP archive structure and manifest
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("Failed to open artifacts zip bundle: %v", err)
	}
	defer zr.Close()

	zipFileMap := make(map[string]*zip.File)
	for _, f := range zr.File {
		zipFileMap[f.Name] = f
	}

	if _, ok := zipFileMap["artifacts/manifest.json"]; !ok {
		t.Errorf("manifest.json missing from ZIP bundle")
	}
	if _, ok := zipFileMap["artifacts/adr/ADR_001_Pure_Go.md"]; !ok {
		t.Errorf("ADR_001_Pure_Go.md missing from ZIP bundle under artifacts/adr/")
	}
	if _, ok := zipFileMap["artifacts/checklists/CHECKLIST_Deploy.md"]; !ok {
		t.Errorf("CHECKLIST_Deploy.md missing from ZIP bundle under artifacts/checklists/")
	}

	// 3. Verify sent Telegram requests (transcript caption enrichment and companion ZIP delivery)
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundEnrichedCaption := false
	foundZipDocument := false

	for _, body := range sentBodies {
		if strings.Contains(body, "Extracted Artifacts:") && strings.Contains(body, "ADR") {
			foundEnrichedCaption = true
		}
		if strings.Contains(body, expectedZipFilename) || strings.Contains(body, "Session Engineering Artifacts Bundle") {
			foundZipDocument = true
		}
	}

	if !foundEnrichedCaption {
		t.Errorf("Expected enriched transcript caption with extracted artifacts breakdown in sent bodies")
	}
	if !foundZipDocument {
		t.Errorf("Expected companion ZIP document to be sent to Telegram")
	}
}

func TestHandleExportCommand_SafeParking_HeadlessExport(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	sessionID := "safe-parking-headless-99"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	_ = os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Safe Park Session"), 0644)
	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Execute work</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Working..."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)
	_ = os.WriteFile(filepath.Join(sessionDir, "research_metrics.md"), []byte("# Research on Metrics\nAnalysis content."), 0644)

	user := User{
		ID:        1001,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	// In Safe Parking with nil bot and chatID 0, it must execute cleanly without panic
	handleExportCommand(nil, 0, 1001, "HeadlessBot", user)

	exportDir := filepath.Join(mockAgents, "HeadlessBot", "scratch", "exports")
	expectedMdFilename := fmt.Sprintf("session_Safe_Park_Session_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_Safe_Park_Session_%s.zip", safePrefix(sessionID, 8))

	if _, err := os.Stat(filepath.Join(exportDir, expectedMdFilename)); err != nil {
		t.Errorf("Headless export failed to generate markdown transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, expectedZipFilename)); err != nil {
		t.Errorf("Headless export failed to generate artifacts zip bundle: %v", err)
	}
}

func TestHandleExportCommand_NoArtifacts_FallbackSingleTranscript(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "empty-artifacts-55"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>No files created</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Done."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)

	user := User{
		ID:        555,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 998877, 555, "CleanBot", user)

	exportDir := filepath.Join(mockAgents, "CleanBot", "scratch", "exports")
	expectedMdFilename := fmt.Sprintf("session_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_%s.zip", safePrefix(sessionID, 8))

	if _, err := os.Stat(filepath.Join(exportDir, expectedMdFilename)); err != nil {
		t.Errorf("Transcript markdown missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, expectedZipFilename)); err == nil {
		t.Errorf("Did not expect artifacts ZIP to exist when no artifacts were created")
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	for _, body := range sentBodies {
		if strings.Contains(body, "Extracted Artifacts:") {
			t.Errorf("Caption unexpectedly mentioned Extracted Artifacts when count was 0")
		}
	}
}
