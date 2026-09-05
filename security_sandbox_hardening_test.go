package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/mattn/go-sqlite3"
)

func TestIsPathUnderRoot(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		root     string
		expected bool
	}{
		{"Empty path", "", "/root/projects", false},
		{"Empty root", "/root/projects", "", false},
		{"Exact match", "/root/projects", "/root/projects", true},
		{"Valid subfile", "/root/projects/app/main.go", "/root/projects", true},
		{"Valid nested subdir", "/root/projects/deep/nested/dir/file.txt", "/root/projects", true},
		{"Traversal escaping root", "/root/projects/../../etc/passwd", "/root/projects", false},
		{"Prefix name collision", "/root/projects-fake/file.txt", "/root/projects", false},
		{"Relative traversal escaping root", "../../../../../etc/passwd", "/root/projects", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isPathUnderRoot(tc.path, tc.root)
			if got != tc.expected {
				t.Errorf("isPathUnderRoot(%q, %q) = %v, expected %v", tc.path, tc.root, got, tc.expected)
			}
		})
	}
}

func TestIsValidSessionID(t *testing.T) {
	tests := []struct {
		name     string
		sid      string
		expected bool
	}{
		{"Valid UUID", "d222263c-33ac-4d27-921a-d4073bc40c07", true},
		{"Valid alphanumeric", "session_123_abc-456", true},
		{"Empty string", "", false},
		{"Whitespace only", "   ", false},
		{"Path traversal slash", "session/123", false},
		{"Path traversal dot-dot", "../etc/passwd", false},
		{"Command injection chars", "session;rm -rf /", false},
		{"Newline injection", "session\nid", false},
		{"Null byte", "session\x00id", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidSessionID(tc.sid)
			if got != tc.expected {
				t.Errorf("isValidSessionID(%q) = %v, expected %v", tc.sid, got, tc.expected)
			}
		})
	}
}

func TestEnsureEnvPermissions(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current wd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	t.Setenv("ALLOW_DOTENV", "1")

	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte("TEST=1\n"), 0644); err != nil {
		t.Fatalf("Failed to create test .env: %v", err)
	}

	ensureEnvPermissions()

	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatalf("Failed to stat .env: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("Expected .env permissions to be 0600, got %o", mode)
	}
}

func TestHandleWorkspaceCommand_Security(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ws_sec_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	projectsDir := filepath.Join(tempDir, "projects")
	agentsDir := filepath.Join(tempDir, "agents")
	os.MkdirAll(projectsDir, 0755)
	os.MkdirAll(filepath.Join(agentsDir, "TestBot"), 0755)
	os.MkdirAll(filepath.Join(agentsDir, "OtherBot"), 0755)

	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3001, '/initial', 'gemini-3.7-flash-high', 0, 'session_1', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	bot := &tgbotapi.BotAPI{}
	botName := "TestBot"
	user := User{ID: 3001, Workspace: "/initial", Model: "gemini-3.7-flash-high"}

	// 1. Valid workspace in PROJECTS_DIR
	validProj := filepath.Join(projectsDir, "my_app")
	os.MkdirAll(validProj, 0755)
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+validProj, botName, user, db)

	var ws1 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws1)
	if ws1 != validProj {
		t.Errorf("Expected workspace in DB to be updated to %s, got %s", validProj, ws1)
	}

	// 2. Forbidden workspace in OtherBot's office
	forbiddenOffice := filepath.Join(agentsDir, "OtherBot")
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+forbiddenOffice, botName, user, db)

	var ws2 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws2)
	if ws2 != validProj {
		t.Errorf("Expected workspace update to OtherBot office to be blocked, but DB was changed to %s", ws2)
	}

	// 3. Forbidden workspace in /etc
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace /etc", botName, user, db)
	var ws3 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws3)
	if ws3 != validProj {
		t.Errorf("Expected workspace update to /etc to be blocked, but DB was changed to %s", ws3)
	}
}
