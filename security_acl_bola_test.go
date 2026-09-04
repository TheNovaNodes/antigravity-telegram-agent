package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestStorage_UpdateUserSession_RejectsEmptyOrInvalidSessionID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(4001)

	// 1. Initial valid insert
	updateUserSession(db, userID, "valid-session-123")

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("Expected 1 session in history, got %d, err: %v", count, err)
	}

	// 2. Empty session ID should NOT insert into session_history
	updateUserSession(db, userID, "")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Empty session ID should not be inserted, expected count 1, got %d", count)
	}

	// 3. Invalid/Traversal session ID should NOT insert into session_history
	updateUserSession(db, userID, "../../etc/passwd")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Invalid session ID should not be inserted, expected count 1, got %d", count)
	}

	// 4. Another valid session
	updateUserSession(db, userID, "another-valid-session_456")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 2 {
		t.Errorf("Expected 2 valid sessions, got %d", count)
	}
}

func TestIsSessionOwnedByUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userA := int64(1001)
	userB := int64(2002)

	updateUserSession(db, userA, "session-owned-by-a")
	updateUserSession(db, userB, "session-owned-by-b")

	// Verify User A owns their session
	if !isSessionOwnedByUser(db, userA, "session-owned-by-a") {
		t.Errorf("User A should own session-owned-by-a")
	}

	// Verify User A does NOT own User B's session
	if isSessionOwnedByUser(db, userA, "session-owned-by-b") {
		t.Errorf("User A must NOT own session-owned-by-b (BOLA vulnerability)")
	}

	// Verify non-existent session
	if isSessionOwnedByUser(db, userA, "nonexistent-session") {
		t.Errorf("Nonexistent session must return false")
	}

	// Verify empty session
	if isSessionOwnedByUser(db, userA, "") {
		t.Errorf("Empty session must return false")
	}

	// Verify nil DB
	if isSessionOwnedByUser(nil, userA, "session-owned-by-a") {
		t.Errorf("Nil DB must return false")
	}
}

func TestBOLA_ResumeCallback_RejectionAndAllowance(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	userVictimID := int64(888)
	userAttackerID := int64(999)

	// Initialize victim and attacker in DB
	victimUser := getUser(db, userVictimID, "TestMockBot")
	attackerUser := getUser(db, userAttackerID, "TestMockBot")

	// Victim created a session
	updateUserSession(db, userVictimID, "victim-private-session-123")

	// Attacker tries to resume victim's session via forged callback query
	cbAttacker := &tgbotapi.CallbackQuery{
		ID:   "cb_attack_1",
		From: &tgbotapi.User{ID: userAttackerID, UserName: "attacker"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 101,
		},
		Data: "resume:victim-private-session-123",
	}

	handleCallbackQuery(bot, cbAttacker, attackerUser, "TestMockBot", db)

	// Attacker user session in DB must NOT be changed to victim's session
	checkAttacker := getUser(db, userAttackerID, "TestMockBot")
	if checkAttacker.SessionID == "victim-private-session-123" {
		t.Fatalf("BOLA breach! Attacker was able to resume victim's session: %s", checkAttacker.SessionID)
	}

	// Verify invalid session ID is rejected
	cbInvalid := &tgbotapi.CallbackQuery{
		ID:   "cb_invalid_1",
		From: &tgbotapi.User{ID: userVictimID, UserName: "victim"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 54321},
			MessageID: 102,
		},
		Data: "resume:../../etc/shadow",
	}
	handleCallbackQuery(bot, cbInvalid, victimUser, "TestMockBot", db)

	// Now legitimate user resumes their own session
	cbVictim := &tgbotapi.CallbackQuery{
		ID:   "cb_legit_1",
		From: &tgbotapi.User{ID: userVictimID, UserName: "victim"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 54321},
			MessageID: 103,
		},
		Data: "resume:victim-private-session-123",
	}

	handleCallbackQuery(bot, cbVictim, victimUser, "TestMockBot", db)

	// Victim's session should now be active
	checkVictim := getUser(db, userVictimID, "TestMockBot")
	if checkVictim.SessionID == "" {
		t.Errorf("Expected victim session to be active, got empty")
	}
}

func TestDownloadTelegramMedia_ErrorHandlingAndStatus(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	// File server returning 404
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer errServer.Close()

	// Mock Telegram API server returning the errServer URL
	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"file404","file_path":"%s"}}`, errServer.URL+"/missing.txt")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	_, _, err := downloadTelegramMedia(bot, 12345, "file404", ".txt", "test", "", "TestBot")
	if err == nil {
		t.Errorf("Expected error downloading 404 media from server, got nil")
	}
}
