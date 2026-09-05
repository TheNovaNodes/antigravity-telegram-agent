package main

import (
	"database/sql"
	"testing"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func TestSafePrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"Empty string", "", 8, ""},
		{"Short ASCII", "abc", 8, "abc"},
		{"Exact ASCII", "12345678", 8, "12345678"},
		{"Long ASCII", "1234567890abcdef", 8, "12345678"},
		{"Short Cyrillic", "привет", 8, "привет"},
		{"Long Cyrillic", "приветмиртест", 6, "привет"},
		{"Emojis", "🎭🤖💬⚡🚀🔥", 3, "🎭🤖💬"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safePrefix(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Errorf("safePrefix(%q, %d) = %q, expected %q", tc.input, tc.maxLen, got, tc.expected)
			}
		})
	}
}

func TestTruncateUTF8Bytes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
	}{
		{"Empty", "", 64},
		{"ASCII short", "hello world", 64},
		{"ASCII cut", "012345678901234567890123456789", 10},
		{"Cyrillic split byte", "Привет, мир! Это тестовая строка на русском языке", 15}, // 15 bytes cuts mid-rune
		{"Emoji split byte", "🤖🎭💬⚡🚀✨🔥", 10},                                              // 10 bytes cuts mid-4byte-emoji
		{"Max 64 bytes limit", "resume:session_custom_title_with_lots_of_words_and_utf8_тест_длинного_названия", 64},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8Bytes(tc.input, tc.maxBytes)
			if len([]byte(got)) > tc.maxBytes {
				t.Errorf("truncateUTF8Bytes(%q, %d) byte length %d exceeds maxBytes %d", tc.input, tc.maxBytes, len([]byte(got)), tc.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateUTF8Bytes(%q, %d) produced invalid UTF-8: %q (bytes: %v)", tc.input, tc.maxBytes, got, []byte(got))
			}
		})
	}
}

func TestHandleCommand_PrefixCollisionProtection(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	bot := &tgbotapi.BotAPI{}
	botName := "TestAgentBot"
	user := User{ID: 1001, Workspace: "/root", Model: "gemini-3.7-flash-high"}

	// Valid commands that SHOULD be handled
	validCommands := []string{
		"/start",
		"/start@TestAgentBot",
		"/resume",
		"/resume@testagentbot",
		"/tts hello",
		"/voice on",
		"/workspace /root",
		"/rename new title",
		"/export",
		"/usage",
		"/help",
		"/model",
		"/refresh_models",
		"/clear",
	}

	for _, cmd := range validCommands {
		handled := handleCommand(bot, 12345, 1001, cmd, botName, user, db)
		if !handled {
			t.Errorf("Expected valid command %q to be handled (return true), got false", cmd)
		}
	}

	// Pseudo-commands (prefix collisions) that MUST NOT be handled
	collisionCommands := []string{
		"/workspacex",
		"/workspace_test",
		"/voiceover",
		"/voicemail",
		"/ttspayload",
		"/ttsspeak",
		"/renamed",
		"/export_data",
		"/usagereport",
		"/cleartoend",
	}

	for _, cmd := range collisionCommands {
		handled := handleCommand(bot, 12345, 1001, cmd, botName, user, db)
		if handled {
			t.Errorf("Expected collision command %q to NOT be handled (return false), but got true", cmd)
		}
	}
}

func TestHandleExportCommand_EmptySessionID_NoPanic(t *testing.T) {
	bot := &tgbotapi.BotAPI{}
	user := User{ID: 1002, SessionID: "", Workspace: "/root", Model: "gemini-3.7-flash-high"}

	// Must not panic on empty SessionID
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleExportCommand panicked with empty SessionID: %v", r)
		}
	}()

	handleExportCommand(bot, 12345, 1002, "TestBot", user)
}

func TestHandleClearCommand_FreshSession(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (1003, '/root', 'gemini-3.7-flash-high', 0, 'old_session_123', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	bot := &tgbotapi.BotAPI{}
	user := User{ID: 1003, SessionID: "old_session_123", Workspace: "/root", Model: "gemini-3.7-flash-high"}

	handleClearCommand(bot, 12345, 1003, "TestBot", user, db)

	// Verify DB was updated with empty session
	var updatedSession string
	if err := db.QueryRow("SELECT session_id FROM users WHERE user_id = 1003").Scan(&updatedSession); err != nil {
		t.Fatalf("Failed to query user session: %v", err)
	}
	if updatedSession != "" {
		t.Errorf("Expected session_id in DB to be cleared to empty string, got %q", updatedSession)
	}
}
