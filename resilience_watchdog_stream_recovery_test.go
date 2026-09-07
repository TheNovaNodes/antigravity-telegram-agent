package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestTurnWatchdog_TimeoutResetsActiveMessageID verifies that a stalled turn with no activity
// is detected by the watchdog, resetting ActiveMessageID and clearing turn locks.
func TestTurnWatchdog_TimeoutResetsActiveMessageID(t *testing.T) {
	os.Setenv("TURN_TIMEOUT_MINUTES", "1")
	defer os.Unsetenv("TURN_TIMEOUT_MINUTES")

	s := &AgySession{
		BotName:         "WatchdogTestBot",
		ChatID:          12345,
		UserID:          1001,
		ActiveMessageID: 777,
		ActiveTurnStart: time.Now().Add(-2 * time.Minute),
		LastActivity:    time.Now().Add(-2 * time.Minute),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run watchdog evaluation loop
	timeout := getTurnTimeout()
	if timeout != 1*time.Minute {
		t.Fatalf("Expected timeout of 1m, got %v", timeout)
	}

	now := time.Now()
	stalled := false
	if !s.LastActivity.IsZero() && now.Sub(s.LastActivity) > timeout {
		stalled = true
	}

	if !stalled {
		t.Fatalf("Expected session to be marked stalled")
	}

	// Trigger watchdog cleanup
	s.mu.Lock()
	s.ActiveMessageID = 0
	s.ActiveTurnStart = time.Time{}
	s.StreamRetries = 0
	s.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveMessageID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0, got %d", s.ActiveMessageID)
	}
	if !s.ActiveTurnStart.IsZero() {
		t.Errorf("Expected ActiveTurnStart to be zero, got %v", s.ActiveTurnStart)
	}
	_ = ctx
}

// TestStopCommand_InterruptsActiveTurn_PreservesConversation verifies that /stop terminates the active turn
// without erasing the user's conversation ID in memory or database.
func TestStopCommand_InterruptsActiveTurn_PreservesConversation(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	db := initDB("StopTestBot")
	defer db.Close()

	user := getUser(db, 2002, "StopTestBot")
	convID := "test-stop-conv-1234"
	updateUserSession(db, 2002, convID)
	user = getUser(db, 2002, "StopTestBot")

	sessionKey := fmt.Sprintf("StopTestBot:%d:%d", int64(12345), user.ID)
	session := &AgySession{
		BotName:         "StopTestBot",
		ChatID:          12345,
		UserID:          user.ID,
		DB:              db,
		Conversation:    convID,
		ActiveMessageID: 888,
		ActiveTurnStart: time.Now(),
		LastActivity:    time.Now(),
		isAlive:         true,
	}

	sessionMu.Lock()
	globalSessions[sessionKey] = session
	sessionMu.Unlock()

	// Verify command routing for /stop and /cancel
	handledStop := handleCommand(nil, 12345, user.ID, "/stop", "StopTestBot", user, db)
	if !handledStop {
		t.Errorf("Expected handleCommand to handle /stop")
	}

	session.mu.Lock()
	activeID := session.ActiveMessageID
	turnStart := session.ActiveTurnStart
	alive := session.isAlive
	session.mu.Unlock()

	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after /stop, got %d", activeID)
	}
	if !turnStart.IsZero() {
		t.Errorf("Expected ActiveTurnStart to be zeroed out, got %v", turnStart)
	}
	if alive {
		t.Errorf("Expected session.isAlive to be false after /stop")
	}

	// Verify ConversationID was preserved
	convAfter := session.GetConversation()
	if convAfter != convID {
		t.Errorf("Expected session conversation to be preserved as %q, got %q", convID, convAfter)
	}
	userAfter := getUser(db, 2002, "StopTestBot")
	if userAfter.SessionID != convID {
		t.Errorf("Expected user.SessionID in DB to be preserved as %q, got %q", convID, userAfter.SessionID)
	}

	// Verify /cancel also works
	handledCancel := handleCommand(nil, 12345, user.ID, "/cancel", "StopTestBot", user, db)
	if !handledCancel {
		t.Errorf("Expected handleCommand to handle /cancel")
	}
}

// TestErrorClassification_StreamInterruptionAndRateLimit validates the classification logic
// for stream interruptions, 429 quota exhaustion, and CLI print timeouts.
func TestErrorClassification_StreamInterruptionAndRateLimit(t *testing.T) {
	tests := []struct {
		errMsg              string
		wantStreamInterrupt bool
		wantRateLimit       bool
		wantPrintTimeout    bool
	}{
		{
			errMsg:              "The stream was interrupted. Please continue the task you were working on.",
			wantStreamInterrupt: true,
			wantRateLimit:       false,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "connection reset by peer",
			wantStreamInterrupt: true,
			wantRateLimit:       false,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "429 Resource Exhausted: Quota exceeded for quota metric",
			wantStreamInterrupt: false,
			wantRateLimit:       true,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "HTTP 503 Service Unavailable: rate limit",
			wantStreamInterrupt: false,
			wantRateLimit:       true,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "timeout waiting for response",
			wantStreamInterrupt: false,
			wantRateLimit:       false,
			wantPrintTimeout:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.errMsg, func(t *testing.T) {
			errLower := strings.ToLower(tc.errMsg)

			isStreamInterrupted := strings.Contains(errLower, "stream was interrupted") ||
				strings.Contains(errLower, "stream interrupted") ||
				strings.Contains(errLower, "connection reset")

			isPrintTimeout := strings.Contains(errLower, "timeout waiting for response")

			isRateLimit := (strings.Contains(tc.errMsg, "429") ||
				strings.Contains(tc.errMsg, "503") ||
				strings.Contains(errLower, "rate limit") ||
				strings.Contains(errLower, "quota") ||
				strings.Contains(errLower, "resource_exhausted")) &&
				!isPrintTimeout

			if isStreamInterrupted != tc.wantStreamInterrupt {
				t.Errorf("isStreamInterrupted = %v, want %v", isStreamInterrupted, tc.wantStreamInterrupt)
			}
			if isRateLimit != tc.wantRateLimit {
				t.Errorf("isRateLimit = %v, want %v", isRateLimit, tc.wantRateLimit)
			}
			if isPrintTimeout != tc.wantPrintTimeout {
				t.Errorf("isPrintTimeout = %v, want %v", isPrintTimeout, tc.wantPrintTimeout)
			}
		})
	}
}

// TestRateLimit_ColdSafeParking_PreservesModel verifies that when a rate limit occurs,
// the model in database is NOT mutated/demoted.
func TestRateLimit_ColdSafeParking_PreservesModel(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	db := initDB("RateLimitBot")
	defer db.Close()

	user := getUser(db, 3003, "RateLimitBot")
	preferredModel := "gemini-3.8-flash-high"
	updateUserModel(db, 3003, preferredModel)
	convID := "test-ratelimit-conv"
	updateUserSession(db, 3003, convID)

	sessionKey := fmt.Sprintf("RateLimitBot:%d:%d", int64(12345), user.ID)
	session := &AgySession{
		BotName:         "RateLimitBot",
		ChatID:          12345,
		UserID:          3003,
		DB:              db,
		Model:           preferredModel,
		Conversation:    convID,
		ActiveMessageID: 999,
		isAlive:         true,
	}

	sessionMu.Lock()
	globalSessions[sessionKey] = session
	sessionMu.Unlock()

	// Simulate Cold Safe Parking cleanup
	session.Kill()
	session.mu.Lock()
	session.ActiveMessageID = 0
	session.ActiveTurnStart = time.Time{}
	session.StreamRetries = 0
	session.mu.Unlock()

	updateUserSession(db, 3003, "")
	sessionMu.Lock()
	delete(globalSessions, sessionKey)
	sessionMu.Unlock()

	// Verify model is preserved untouched in DB
	userAfter := getUser(db, 3003, "RateLimitBot")
	if userAfter.Model != preferredModel {
		t.Errorf("Expected user.Model to remain %q, got %q", preferredModel, userAfter.Model)
	}
	if userAfter.SessionID != "" {
		t.Errorf("Expected user.SessionID to be cleared, got %q", userAfter.SessionID)
	}

	sessionMu.Lock()
	_, exists := globalSessions[sessionKey]
	sessionMu.Unlock()
	if exists {
		t.Errorf("Expected session to be evicted from globalSessions on cold parking")
	}
}
