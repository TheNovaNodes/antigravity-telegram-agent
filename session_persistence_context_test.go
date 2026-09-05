package main

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

func TestGetSession_AttachesDBAndPersistsInitEvent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88881)
	chatID := int64(88881)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)
	initialSessionID := user.SessionID

	// Call getSession passing the db handle
	session := getSession(botName, user, chatID, db)
	if session == nil {
		t.Fatal("Expected session to be created, got nil")
	}
	if session.DB == nil {
		t.Error("Expected session.DB to be attached and non-nil")
	}

	// Stop background process started by getSession before testing mock stdout stream
	session.Kill()

	// Simulate agy outputting an init event with the actual conversation ID
	realConvID := "agy-real-conversation-uuid-777"
	jsonl := `{"event":"init","conversation_id":"` + realConvID + `"}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session.mu.Lock()
	session.ctx = ctx
	session.cancel = cancel
	session.isAlive = true
	session.StdoutScanner = scanner
	session.mu.Unlock()

	session.readStdoutLoop()

	if session.GetConversation() != realConvID {
		t.Errorf("Expected session conversation to be %s, got %s", realConvID, session.GetConversation())
	}

	// Verify database was automatically updated
	var dbSessionID string
	err := db.QueryRow("SELECT session_id FROM users WHERE user_id = ?", userID).Scan(&dbSessionID)
	if err != nil {
		t.Fatalf("Failed to query user from DB: %v", err)
	}
	if dbSessionID != realConvID {
		t.Errorf("Expected DB session_id to be %s, got %s (initial was %s)", realConvID, dbSessionID, initialSessionID)
	}

	// Verify session history has the real conversation ID
	var histCount int
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ? AND session_id = ?", userID, realConvID).Scan(&histCount)
	if err != nil {
		t.Fatalf("Failed to query session_history: %v", err)
	}
	if histCount == 0 {
		t.Errorf("Expected session_history to contain %s", realConvID)
	}
}

func TestGetSession_MultiTurnContextRetention_NoProcessKill(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88882)
	chatID := int64(88882)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)

	// Turn 1: Initial creation
	session1 := getSession(botName, user, chatID, db)
	if session1 == nil {
		t.Fatal("Expected turn 1 session to be created")
	}
	session1.Kill()

	// Simulate init event updating conversation
	realConvID := "conv-multi-turn-999"
	jsonl := `{"event":"init","conversation_id":"` + realConvID + `"}` + "\n"
	scanner1 := bufio.NewScanner(strings.NewReader(jsonl))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	session1.mu.Lock()
	session1.ctx = ctx1
	session1.cancel = cancel1
	session1.isAlive = true
	session1.StdoutScanner = scanner1
	session1.mu.Unlock()
	session1.readStdoutLoop()

	// Turn 2: User sends another message. getUser loads the updated session_id
	userTurn2 := getUser(db, userID, botName)
	if userTurn2.SessionID != realConvID {
		t.Fatalf("Expected userTurn2.SessionID to be %s, got %s", realConvID, userTurn2.SessionID)
	}

	session2 := getSession(botName, userTurn2, chatID, db)
	if session2 != session1 {
		t.Errorf("Expected Turn 2 to retain identical session instance (session2 != session1)")
	}
	if session1.ctx.Err() != nil {
		t.Errorf("Expected session1 context not to be cancelled, got %v", session1.ctx.Err())
	}
	if session2.GetConversation() != realConvID {
		t.Errorf("Expected session2 conversation to remain %s, got %s", realConvID, session2.GetConversation())
	}

	// Turn 3: User sends another message even if user struct has empty sessionID
	userTurn3 := userTurn2
	userTurn3.SessionID = ""
	session3 := getSession(botName, userTurn3, chatID, db)
	if session3 != session1 {
		t.Errorf("Expected Turn 3 with empty sessionID in User to retain active session instance")
	}
}

func TestGetSession_ExplicitSessionSwitch_KillsOldSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(88883)
	chatID := int64(88883)
	botName := "TestPersistenceBot"

	user := getUser(db, userID, botName)
	user.SessionID = "conv-first-111"

	session1 := getSession(botName, user, chatID, db)
	if session1 == nil {
		t.Fatal("Expected session1 to be created")
	}
	session1.mu.Lock()
	session1.isAlive = true
	session1.mu.Unlock()

	// User explicitly switches to a different session
	userSwitched := user
	userSwitched.SessionID = "conv-second-222"

	session2 := getSession(botName, userSwitched, chatID, db)
	if session2 == session1 {
		t.Errorf("Expected different session instance after explicit session ID switch")
	}
	if session1.ctx.Err() != context.Canceled {
		t.Errorf("Expected old session context to be canceled with context.Canceled, got %v", session1.ctx.Err())
	}
	if session2.GetConversation() != "conv-second-222" {
		t.Errorf("Expected new session conversation to be conv-second-222, got %s", session2.GetConversation())
	}
}
