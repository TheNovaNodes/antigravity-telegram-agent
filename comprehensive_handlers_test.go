package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

func TestPathHelpers_EnvironmentOverrides(t *testing.T) {
	tempDir := t.TempDir()

	customAgents := filepath.Join(tempDir, "custom_agents")
	customBrain := filepath.Join(tempDir, "custom_brain")
	customAgy := filepath.Join(tempDir, "custom_agy")

	t.Setenv("AGENTS_DIR", customAgents)
	t.Setenv("BRAIN_DIR", customBrain)
	t.Setenv("AGY_BINARY", customAgy)

	if got := getAgentsDir(); got != customAgents {
		t.Errorf("Expected getAgentsDir %s, got %s", customAgents, got)
	}
	if got := getBrainDir(); got != customBrain {
		t.Errorf("Expected getBrainDir %s, got %s", customBrain, got)
	}
	if got := getAgyPath(); got != customAgy {
		t.Errorf("Expected getAgyPath %s, got %s", customAgy, got)
	}
}

func TestAgySession_SetAndGetConversation(t *testing.T) {
	s := &AgySession{
		BotName:      "TestBot",
		Conversation: "initial-conv",
	}

	if got := s.GetConversation(); got != "initial-conv" {
		t.Errorf("Expected initial-conv, got %s", got)
	}

	s.SetConversation("updated-conv")
	if got := s.GetConversation(); got != "updated-conv" {
		t.Errorf("Expected updated-conv, got %s", got)
	}
}

func TestHandleStartCommand_WithTranscriptAndTitle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	user := User{
		ID:         999,
		Workspace:  "/tmp/test_workspace",
		Model:      "gemini-3.8-flash-high",
		SessionID:  "test-session-uuid",
		VoiceReply: true,
	}

	// Create session directory with title and transcript
	sessionDir := filepath.Join(tempDir, user.SessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)

	titleFile := filepath.Join(sessionDir, ".title")
	os.WriteFile(titleFile, []byte("My Custom Session Title"), 0644)

	transcriptFile := filepath.Join(logsDir, "transcript.jsonl")
	twoHoursAgo := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	transcriptContent := fmt.Sprintf(`{"step":1,"created_at":"%s","content":"First step"}
{"step":2,"created_at":"%s","content":"Second step"}
`, twoHoursAgo, time.Now().Format(time.RFC3339))
	os.WriteFile(transcriptFile, []byte(transcriptContent), 0644)

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected Telegram message to be sent for handleStartCommand")
	}
}

func TestHandleResumeCommand_WithActiveTranscripts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(999)

	// Create 2 test sessions in DB
	sid1 := uuid.New().String()
	sid2 := uuid.New().String()

	updateUserSession(db, userID, sid1)
	updateUserSession(db, userID, sid2)

	// Session 1: with .title file
	sdir1 := filepath.Join(tempDir, sid1, ".system_generated", "logs")
	os.MkdirAll(sdir1, 0755)
	os.WriteFile(filepath.Join(tempDir, sid1, ".title"), []byte("Session With Title"), 0644)
	os.WriteFile(filepath.Join(sdir1, "transcript.jsonl"), []byte(`{"content":"hello"}`), 0644)

	// Session 2: with <USER_REQUEST> in transcript
	sdir2 := filepath.Join(tempDir, sid2, ".system_generated", "logs")
	os.MkdirAll(sdir2, 0755)
	transcriptJSON := `{"content":"<USER_REQUEST>\nFix the data race in engine\n</USER_REQUEST>"}`
	os.WriteFile(filepath.Join(sdir2, "transcript.jsonl"), []byte(transcriptJSON), 0644)

	handleResumeCommand(bot, chatID, userID, db)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected Telegram resume menu to be sent")
	}
}

func TestHandleRenameCommand_ActiveAndEmpty(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	user := User{
		ID:        999,
		Workspace: "/tmp/test",
		Model:     "gemini-3.8-flash-high",
		SessionID: "rename-test-uuid",
	}

	// 1. Rename with empty name argument
	handleRenameCommand(bot, chatID, "/rename", "TestMockBot", user)

	// 2. Rename with active session
	session := getSession("TestMockBot", user, chatID)
	session.SetConversation("rename-test-uuid")

	handleRenameCommand(bot, chatID, "/rename Project Alpha Dashboard", "TestMockBot", user)

	titleFile := filepath.Join(tempDir, "rename-test-uuid", ".title")
	content, err := os.ReadFile(titleFile)
	if err != nil || string(content) != "Project Alpha Dashboard" {
		t.Errorf("Expected title file content 'Project Alpha Dashboard', got '%s', err: %v", string(content), err)
	}

	// 3. Rename with no active conversation
	session.SetConversation("")
	handleRenameCommand(bot, chatID, "/rename New Title", "TestMockBot", user)
}

func TestHandleWorkspaceCommand_ValidAndInvalid(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgents, 0755)
	t.Setenv("AGENTS_DIR", mockAgents)

	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(999)
	user := getUser(db, userID, "TestMockBot")

	// 1. Missing arg
	handleWorkspaceCommand(bot, chatID, userID, "/workspace", "TestMockBot", user, db)

	// 2. Relative path
	handleWorkspaceCommand(bot, chatID, userID, "/workspace relative/path", "TestMockBot", user, db)

	// 3. Path outside allowed root
	forbiddenDir := filepath.Join(os.TempDir(), "forbidden_system_dir")
	os.MkdirAll(forbiddenDir, 0755)
	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+forbiddenDir, "TestMockBot", user, db)

	// 4. Valid path inside bot office
	validSubLab := filepath.Join(mockAgents, "TestMockBot", "sublab_1")
	os.MkdirAll(validSubLab, 0755)
	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+validSubLab, "TestMockBot", user, db)

	updatedUser := getUser(db, userID, "TestMockBot")
	if updatedUser.Workspace != validSubLab {
		t.Errorf("Expected updated workspace %s, got %s", validSubLab, updatedUser.Workspace)
	}
}

func TestDownloadTelegramMedia_WithRealServer(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	// File server hosting the file
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("binary data mock file"))
	}))
	defer fileServer.Close()

	// Mock Telegram API server
	ms := newMockServer()
	defer ms.Close()

	// Update handler to respond to getFile
	ms.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"file123","file_path":"%s"}}`, fileServer.URL+"/test.txt")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	// Test 1: Empty file ID
	formatted, isFile, err := downloadTelegramMedia(bot, 12345, "", ".txt", "plain text", "", "TestBot")
	if err != nil || isFile || formatted != "plain text" {
		t.Errorf("Expected plain text passthrough, got formatted: %s, isFile: %v, err: %v", formatted, isFile, err)
	}

	// Test 2: Valid file download
	formatted, isFile, err = downloadTelegramMedia(bot, 12345, "file123", ".txt", "my note", "my caption", "TestBot")
	if err != nil || !isFile || !strings.Contains(formatted, "[Attached File: file://") {
		t.Errorf("Expected valid attached file string, got formatted: %s, isFile: %v, err: %v", formatted, isFile, err)
	}
}

func TestRegisterBotCommands(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	registerBotCommands(bot)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected registerBotCommands to send SetMyCommands request")
	}
}

func TestSendArtifacts_RealFile(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgents, 0755)
	t.Setenv("AGENTS_DIR", mockAgents)

	artifactPath := filepath.Join(mockAgents, "report.pdf")
	os.WriteFile(artifactPath, []byte("PDF content"), 0644)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	text := fmt.Sprintf("Here is your output: [report](file://%s)", artifactPath)
	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected sendArtifacts to send document to Telegram")
	}
}

func TestSendTypingAction(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	s := &AgySession{
		BotName:         "TestBot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 100,
		VoiceReply:      false,
	}

	// 1. Typing action text mode
	s.sendTypingAction()

	// 2. Typing action voice mode
	s.VoiceReply = true
	s.sendTypingAction()

	// 3. Inactive session (ActiveMessageID == 0)
	s.ActiveMessageID = 0
	s.sendTypingAction()
}

func TestHandleExportCommand_Scenarios(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)
	t.Setenv("AGENTS_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(888)

	user := User{
		ID:        userID,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: "export-test-uuid",
	}

	// 1. Non-existent transcript
	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// 2. Empty transcript
	sessionDir := filepath.Join(tempDir, user.SessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(""), 0644)
	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// 3. Populated transcript with title and tool calls
	os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Project Matrix Export"), 0644)
	fullJSONL := `{"step":1,"created_at":"2026-09-03T17:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nRefactor the bot architecture\n</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-03T17:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Starting refactoring plan...","tool_calls":[{"name":"view_file","args":{"path":"main.go"}}]}
{"step":3,"created_at":"2026-09-03T17:00:10Z","source":"SYSTEM","type":"SYSTEM_MESSAGE","content":"System alert: tests passed"}
`
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(fullJSONL), 0644)

	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// Test export via handleCommand & handleCallbackQuery
	db := setupTestDB(t)
	defer db.Close()
	handleCommand(bot, chatID, userID, "/export", "TestBot", user, db)

	cb := &tgbotapi.CallbackQuery{
		ID:   "cb123",
		From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{
			MessageID: 10,
			Chat:      &tgbotapi.Chat{ID: chatID},
		},
		Data: "cmd:export",
	}
	handleCallbackQuery(bot, cb, user, "TestBot", db)
}
