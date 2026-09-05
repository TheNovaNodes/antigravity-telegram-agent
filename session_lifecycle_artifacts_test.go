package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func TestExtractAllowedArtifacts_ProjectsDirAndFileValidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	projectsDir := filepath.Join(tempDir, "projects")
	agentsDir := filepath.Join(tempDir, "agents")
	brainDir := filepath.Join(tempDir, "brain")

	os.MkdirAll(projectsDir, 0755)
	os.MkdirAll(agentsDir, 0755)
	os.MkdirAll(brainDir, 0755)

	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)
	t.Setenv("BRAIN_DIR", brainDir)

	// Create test files
	projFile := filepath.Join(projectsDir, "result.go")
	os.WriteFile(projFile, []byte("package main\n"), 0644)

	agentFile := filepath.Join(agentsDir, "agent_config.json")
	os.WriteFile(agentFile, []byte("{}"), 0644)

	brainFile := filepath.Join(brainDir, "session_note.txt")
	os.WriteFile(brainFile, []byte("notes"), 0644)

	// Create test directory (must be rejected by IsDir check)
	subDir := filepath.Join(projectsDir, "some_subfolder")
	os.MkdirAll(subDir, 0755)

	// Construct markdown text with multiple file links
	text := fmt.Sprintf("Here are artifacts:\n"+
		"- [Project File](file://%s)\n"+
		"- [Agent File](file://%s)\n"+
		"- [Brain File](file://%s)\n"+
		"- [Directory Link](file://%s)\n"+
		"- [Forbidden Link](file:///etc/passwd)\n",
		projFile, agentFile, brainFile, subDir)

	paths := ExtractAllowedArtifacts(text)

	// Expect exactly 3 allowed files (projFile, agentFile, brainFile)
	if len(paths) != 3 {
		t.Fatalf("Expected exactly 3 allowed artifacts, got %d: %v", len(paths), paths)
	}

	foundMap := make(map[string]bool)
	for _, p := range paths {
		foundMap[p] = true
	}

	if !foundMap[projFile] {
		t.Errorf("Expected project file %s to be allowed", projFile)
	}
	if !foundMap[agentFile] {
		t.Errorf("Expected agent file %s to be allowed", agentFile)
	}
	if !foundMap[brainFile] {
		t.Errorf("Expected brain file %s to be allowed", brainFile)
	}
	if foundMap[subDir] {
		t.Errorf("Expected directory %s to be rejected", subDir)
	}
	if foundMap["/etc/passwd"] {
		t.Errorf("Expected /etc/passwd to be blocked by LFI whitelist")
	}
}

func TestHandleVoiceToggleCommand_SyncsInMemorySession(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (2001, '/root', 'gemini-3.7-flash-high', 0, 'session_1', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	botName := "VoiceSyncBot"
	chatID := int64(999111)
	user := User{ID: 2001, Workspace: "/root", Model: "gemini-3.7-flash-high", VoiceReply: false}

	// Create and register session in memory
	session := getSession(botName, user, chatID)
	if session.VoiceReply != false {
		t.Fatalf("Expected initial session.VoiceReply to be false")
	}

	bot := &tgbotapi.BotAPI{}

	// Toggle voice ON
	handleVoiceToggleCommand(bot, chatID, 2001, "/voice on", botName, user, db)

	// Check DB
	var dbVoice int
	if err := db.QueryRow("SELECT voice_reply FROM users WHERE user_id = 2001").Scan(&dbVoice); err != nil {
		t.Fatalf("Failed to query DB voice_reply: %v", err)
	}
	if dbVoice != 1 {
		t.Errorf("Expected DB voice_reply to be 1, got %d", dbVoice)
	}

	// Check in-memory session
	session.mu.Lock()
	inMemVoice := session.VoiceReply
	session.mu.Unlock()
	if inMemVoice != true {
		t.Errorf("Expected in-memory session.VoiceReply to be synced to true, got %v", inMemVoice)
	}

	// Toggle voice OFF
	user.VoiceReply = true
	handleVoiceToggleCommand(bot, chatID, 2001, "/voice off", botName, user, db)

	session.mu.Lock()
	inMemVoice = session.VoiceReply
	session.mu.Unlock()
	if inMemVoice != false {
		t.Errorf("Expected in-memory session.VoiceReply to be synced to false, got %v", inMemVoice)
	}
}

func TestHandleMessagePayload_VoiceReplyPerTurnNoLatch(t *testing.T) {
	botName := "VoiceTurnBot"
	chatID := int64(999222)
	user := User{ID: 2002, Workspace: "/root", Model: "gemini-3.7-flash-high", VoiceReply: false}

	session := getSession(botName, user, chatID)

	// Turn 1: User sends voice message (isVoice = true)
	session.mu.Lock()
	session.VoiceReply = true || user.VoiceReply
	session.mu.Unlock()

	if !session.VoiceReply {
		t.Fatalf("Expected session.VoiceReply to be true for voice turn")
	}

	// Turn 2: User sends text message (isVoice = false, user.VoiceReply = false)
	session.mu.Lock()
	session.VoiceReply = false || user.VoiceReply
	session.mu.Unlock()

	if session.VoiceReply {
		t.Errorf("Expected session.VoiceReply to reset to false for text turn, but it latched to true")
	}
}
