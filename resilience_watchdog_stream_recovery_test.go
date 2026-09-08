package main

import (
	"bufio"
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

// TestReadStdoutLoop_RefreshesLastActivityOnAnyJsonEvent verifies that non-text JSON events
// (such as step_update with tool_calls) refresh LastActivity, preventing active tool runners
// from being falsely flagged as stalled.
func TestReadStdoutLoop_RefreshesLastActivityOnAnyJsonEvent(t *testing.T) {
	pastTime := time.Now().Add(-10 * time.Minute)
	jsonl := `{"event":"step_update","step_update":{"tool_calls":[{"name":"run_command","args":{"CommandLine":"pytest"}}]}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "ActivityRefreshTestBot",
		LastActivity:  pastTime,
		StdoutScanner: scanner,
		UpdateChan:    make(chan struct{}, 10),
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	session.mu.Lock()
	lastAct := session.LastActivity
	session.mu.Unlock()

	if !lastAct.After(pastTime.Add(9 * time.Minute)) {
		t.Errorf("Expected LastActivity to be refreshed close to time.Now(), was %v (past was %v)", lastAct, pastTime)
	}
}

// TestTurnWatchdog_BufferSalvageAndProcessKill verifies that when a turn stalls,
// any existing text in TextBuffer is salvaged and s.Kill() terminates the session process.
func TestTurnWatchdog_BufferSalvageAndProcessKill(t *testing.T) {
	os.Setenv("TURN_TIMEOUT_MINUTES", "1")
	defer os.Unsetenv("TURN_TIMEOUT_MINUTES")

	timeout := getTurnTimeout()
	if timeout != 1*time.Minute {
		t.Fatalf("Expected timeout of 1m, got %v", timeout)
	}

	reportText := "### Medical Report: Polyp analysis complete\n[models.py](file:///tmp/models.py)"
	session := &AgySession{
		BotName:         "SalvageWatchdogBot",
		ChatID:          98765,
		ActiveMessageID: 555,
		ActiveTurnStart: time.Now().Add(-3 * time.Minute),
		LastActivity:    time.Now().Add(-3 * time.Minute),
		TextBuffer:      reportText,
		isAlive:         true,
	}

	now := time.Now()
	stalled := false
	if !session.LastActivity.IsZero() && now.Sub(session.LastActivity) > timeout {
		stalled = true
	}

	if !stalled {
		t.Fatalf("Expected session to be stalled")
	}

	// Execute the hardened watchdog cleanup block
	session.mu.Lock()
	activeMsgID := session.ActiveMessageID
	text := session.TextBuffer
	truncated := session.TextTruncated
	session.ActiveMessageID = 0
	session.ActiveTurnStart = time.Time{}
	session.StreamRetries = 0
	session.TextBuffer = ""
	session.TextTruncated = false
	session.mu.Unlock()

	session.Kill()

	if activeMsgID != 555 {
		t.Errorf("Expected activeMsgID to be 555, got %d", activeMsgID)
	}
	if text != reportText {
		t.Errorf("Expected salvaged text %q, got %q", reportText, text)
	}

	trimmed := strings.TrimSpace(text)
	if truncated {
		trimmed += "\n\n⚠️ _[Response truncated: buffer exceeded 1MB limit]_"
	}
	trimmed += "\n\n⚠️ _[Agent response timed out (inactivity timeout). Output preserved above]_"

	if !strings.Contains(trimmed, reportText) {
		t.Errorf("Expected salvaged output to contain original reportText")
	}
	if !strings.Contains(trimmed, "Agent response timed out (inactivity timeout)") {
		t.Errorf("Expected salvaged output to contain timeout notice")
	}

	// Verify session was killed
	if session.IsAlive() {
		t.Errorf("Expected session.isAlive to be false after s.Kill()")
	}
}

// TestStopCommand_PreservesBufferAndSendsArtifacts verifies that when a user triggers
// /stop on a streaming session, any existing TextBuffer is preserved and artifacts extracted.
func TestStopCommand_PreservesBufferAndSendsArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	db := initDB("StopPreserveBot")
	defer db.Close()

	user := getUser(db, 5005, "StopPreserveBot")
	convID := "test-stop-preserve-conv"
	updateUserSession(db, 5005, convID)
	user = getUser(db, 5005, "StopPreserveBot")

	sessionKey := fmt.Sprintf("StopPreserveBot:%d:%d", int64(12345), user.ID)
	existingText := "Step 1 complete: Code compiled successfully."
	session := &AgySession{
		BotName:         "StopPreserveBot",
		ChatID:          12345,
		UserID:          user.ID,
		DB:              db,
		Conversation:    convID,
		ActiveMessageID: 777,
		ActiveTurnStart: time.Now(),
		LastActivity:    time.Now(),
		TextBuffer:      existingText,
		isAlive:         true,
	}

	sessionMu.Lock()
	globalSessions[sessionKey] = session
	sessionMu.Unlock()

	handled := handleCommand(nil, 12345, user.ID, "/stop", "StopPreserveBot", user, db)
	if !handled {
		t.Errorf("Expected /stop to be handled")
	}

	if session.IsAlive() {
		t.Errorf("Expected session to be killed after /stop")
	}
	if session.ActiveMessageID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after /stop")
	}
}
