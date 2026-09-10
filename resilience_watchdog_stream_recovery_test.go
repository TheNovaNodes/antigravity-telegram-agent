package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestTurnWatchdog_TimeoutResetsActiveMessageID verifies that a stalled turn with no activity
// is detected by the watchdog via checkTurnInactivity, resetting ActiveMessageID and clearing turn locks.
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
		isAlive:         true,
	}

	timeout := getTurnTimeout()
	if timeout != 1*time.Minute {
		t.Fatalf("Expected timeout of 1m, got %v", timeout)
	}

	// 1. Stalled turn must be detected and handled by production checkTurnInactivity
	stalled := s.checkTurnInactivity(time.Now(), timeout)
	if !stalled {
		t.Fatalf("Expected checkTurnInactivity to return true for stalled turn")
	}

	s.mu.Lock()
	activeID := s.ActiveMessageID
	turnStart := s.ActiveTurnStart
	alive := s.isAlive
	s.mu.Unlock()

	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after watchdog, got %d", activeID)
	}
	if !turnStart.IsZero() {
		t.Errorf("Expected ActiveTurnStart to be zero, got %v", turnStart)
	}
	if alive {
		t.Errorf("Expected session to be killed, but isAlive is true")
	}

	// 2. Fresh turn with recent activity must NOT stall
	sFresh := &AgySession{
		BotName:         "FreshBot",
		ChatID:          12345,
		UserID:          1001,
		ActiveMessageID: 888,
		ActiveTurnStart: time.Now(),
		LastActivity:    time.Now(),
		isAlive:         true,
	}
	stalledFresh := sFresh.checkTurnInactivity(time.Now(), timeout)
	if stalledFresh {
		t.Errorf("Expected checkTurnInactivity to return false for fresh turn")
	}
	if sFresh.ActiveMessageID != 888 {
		t.Errorf("Expected ActiveMessageID to remain 888, got %d", sFresh.ActiveMessageID)
	}
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
			isStreamInterrupted, isRateLimit, isPrintTimeout := ClassifyAgentError(tc.errMsg)

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
// any existing text in TextBuffer is salvaged and s.Kill() terminates the session process,
// dispatching the salvaged content to Telegram.
func TestTurnWatchdog_BufferSalvageAndProcessKill(t *testing.T) {
	os.Setenv("TURN_TIMEOUT_MINUTES", "1")
	defer os.Unsetenv("TURN_TIMEOUT_MINUTES")

	timeout := getTurnTimeout()
	if timeout != 1*time.Minute {
		t.Fatalf("Expected timeout of 1m, got %v", timeout)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	reportText := "### Medical Report: Polyp analysis complete\n[models.py](file:///tmp/models.py)"
	session := &AgySession{
		BotName:         "SalvageWatchdogBot",
		BotAPI:          bot,
		ChatID:          98765,
		ActiveMessageID: 555,
		ActiveTurnStart: time.Now().Add(-3 * time.Minute),
		LastActivity:    time.Now().Add(-3 * time.Minute),
		TextBuffer:      reportText,
		isAlive:         true,
	}

	// Trigger production checkTurnInactivity
	stalled := session.checkTurnInactivity(time.Now(), timeout)
	if !stalled {
		t.Fatalf("Expected checkTurnInactivity to return true for stalled turn")
	}

	session.mu.Lock()
	activeMsgID := session.ActiveMessageID
	buf := session.TextBuffer
	alive := session.isAlive
	session.mu.Unlock()

	if activeMsgID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after watchdog, got %d", activeMsgID)
	}
	if buf != "" {
		t.Errorf("Expected TextBuffer to be cleared after watchdog salvage, got %q", buf)
	}
	if alive {
		t.Errorf("Expected session.isAlive to be false after s.Kill()")
	}

	// Verify Telegram payload contains salvaged reportText and timeout notice
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected Telegram message to be sent via mock server")
	}

	var foundText, foundNotice bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Polyp analysis complete") {
			foundText = true
		}
		if strings.Contains(unescaped, "Agent response timed out (inactivity timeout)") {
			foundNotice = true
		}
	}

	if !foundText {
		t.Errorf("Expected salvaged body to contain 'Polyp analysis complete', got %v", sentBodies)
	}
	if !foundNotice {
		t.Errorf("Expected salvaged body to contain timeout notice, got %v", sentBodies)
	}
}

// TestStopCommand_PreservesBufferAndSendsArtifacts verifies that when a user triggers
// /stop on a streaming session, any existing TextBuffer is preserved, artifacts extracted,
// and messages dispatched to Telegram.
func TestStopCommand_PreservesBufferAndSendsArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	db := initDB("StopPreserveBot")
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	user := getUser(db, 5005, "StopPreserveBot")
	convID := "test-stop-preserve-conv"
	updateUserSession(db, 5005, convID)
	user = getUser(db, 5005, "StopPreserveBot")

	sessionKey := fmt.Sprintf("StopPreserveBot:%d:%d", int64(12345), user.ID)
	existingText := "Step 1 complete: Code compiled successfully."
	session := &AgySession{
		BotName:         "StopPreserveBot",
		BotAPI:          bot,
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

	handled := handleCommand(bot, 12345, user.ID, "/stop", "StopPreserveBot", user, db)
	if !handled {
		t.Errorf("Expected /stop to be handled")
	}

	if session.IsAlive() {
		t.Errorf("Expected session to be killed after /stop")
	}
	if session.ActiveMessageID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after /stop")
	}

	// Verify Telegram payload received the preserved text and interruption notice
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected Telegram messages to be sent upon /stop")
	}

	var foundPreservedText, foundStopNotice bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Step 1 complete: Code compiled successfully") {
			foundPreservedText = true
		}
		if strings.Contains(unescaped, "Execution interrupted by user") {
			foundStopNotice = true
		}
	}

	if !foundPreservedText {
		t.Errorf("Expected sent message to preserve text %q, got: %v", existingText, sentBodies)
	}
	if !foundStopNotice {
		t.Errorf("Expected sent message to contain stop notice, got: %v", sentBodies)
	}
}

// TestStreamRecovery_BufferSalvageOnRetriesExhausted verifies that when a Google Cloud
// stream interruption occurs and retries are exhausted, the accumulated TextBuffer is
// salvaged, artifacts are dispatched, and the message includes both the text and retry button.
func TestStreamRecovery_BufferSalvageOnRetriesExhausted(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	salvagedReport := "Deployment status: 4 services configured and running in prod."
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"stream was interrupted"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "SalvageRecoveryBot",
		BotAPI:          bot,
		ChatID:          9911,
		ActiveMessageID: 444,
		TextBuffer:      salvagedReport,
		StreamRetries:   2, // Retries exhausted
		StdoutScanner:   scanner,
		UpdateChan:      make(chan struct{}, 10),
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	session.mu.Lock()
	activeID := session.ActiveMessageID
	buf := session.TextBuffer
	retries := session.StreamRetries
	session.mu.Unlock()

	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be reset to 0, got %d", activeID)
	}
	if buf != "" {
		t.Errorf("Expected TextBuffer to be cleared on session after dispatch, got %q", buf)
	}
	if retries != 0 {
		t.Errorf("Expected StreamRetries to be reset to 0, got %d", retries)
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	var foundText, foundNotice, foundRetryMarkup bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "4 services configured and running in prod") {
			foundText = true
		}
		if strings.Contains(unescaped, "Output salvaged above") {
			foundNotice = true
		}
		if strings.Contains(unescaped, "cmd:retry") {
			foundRetryMarkup = true
		}
	}

	if !foundText {
		t.Errorf("Expected sent message to salvage text %q, got: %v", salvagedReport, sentBodies)
	}
	if !foundNotice {
		t.Errorf("Expected sent message to contain stream severed notice, got: %v", sentBodies)
	}
	if !foundRetryMarkup {
		t.Errorf("Expected sent message to include cmd:retry inline button, got: %v", sentBodies)
	}
}

// TestStreamRecovery_PreservesBufferDuringAutoRecovery verifies that when retries < 2,
// the existing text buffer is preserved and the Telegram message includes both existing text
// and the auto-recovering notice rather than wiping it.
func TestStreamRecovery_PreservesBufferDuringAutoRecovery(t *testing.T) {
	os.Setenv("AGY_BINARY", "/bin/true")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	accumulatedText := "Generating phase 1: analyzing components..."
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"stream was interrupted"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "AutoRecoveryPreserveBot",
		BotAPI:          bot,
		ChatID:          9933,
		ActiveMessageID: 666,
		TextBuffer:      accumulatedText,
		StreamRetries:   0,
		StdoutScanner:   scanner,
		UpdateChan:      make(chan struct{}, 10),
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	var foundText, foundRecoveringNotice bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Generating phase 1: analyzing components") {
			foundText = true
		}
		if strings.Contains(unescaped, "Auto-recovering (attempt 1/2)") {
			foundRecoveringNotice = true
		}
	}

	if !foundText {
		t.Errorf("Expected sent message to preserve text %q, got: %v", accumulatedText, sentBodies)
	}
	if !foundRecoveringNotice {
		t.Errorf("Expected sent message to contain auto-recovering notice, got: %v", sentBodies)
	}
}

// TestGenericError_BufferSalvaged verifies that when an agent error occurs (such as print timeout),
// any accumulated TextBuffer is salvaged and displayed with the error notice.
func TestGenericError_BufferSalvaged(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	partialReport := "Step 1 complete: Database initialized."
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"timeout waiting for response"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "GenericErrorSalvageBot",
		BotAPI:          bot,
		ChatID:          9922,
		ActiveMessageID: 555,
		TextBuffer:      partialReport,
		StdoutScanner:   scanner,
		UpdateChan:      make(chan struct{}, 10),
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	session.mu.Lock()
	activeID := session.ActiveMessageID
	buf := session.TextBuffer
	session.mu.Unlock()

	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0, got %d", activeID)
	}
	if buf != "" {
		t.Errorf("Expected TextBuffer to be cleared after dispatch, got %q", buf)
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	var foundText, foundTimeoutNotice bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Step 1 complete: Database initialized") {
			foundText = true
		}
		if strings.Contains(unescaped, "CLI print timeout") {
			foundTimeoutNotice = true
		}
	}

	if !foundText {
		t.Errorf("Expected sent message to preserve partial report %q, got: %v", partialReport, sentBodies)
	}
	if !foundTimeoutNotice {
		t.Errorf("Expected sent message to include print timeout notice, got: %v", sentBodies)
	}
}

// TestTurnWatchdog_DefaultTimeouts verifies that the default inactivity timeout is 15m
// and the hard turn deadline is 45m, and that environment overrides function properly.
func TestTurnWatchdog_DefaultTimeouts(t *testing.T) {
	os.Unsetenv("TURN_INACTIVITY_TIMEOUT_MINUTES")
	os.Unsetenv("TURN_TIMEOUT_MINUTES")
	os.Unsetenv("TURN_HARD_DEADLINE_MINUTES")

	if timeout := getTurnTimeout(); timeout != 15*time.Minute {
		t.Errorf("Expected default turn timeout of 15m, got %v", timeout)
	}
	if deadline := getHardTurnDeadline(); deadline != 45*time.Minute {
		t.Errorf("Expected default hard turn deadline of 45m, got %v", deadline)
	}

	// Test environment overrides
	os.Setenv("TURN_INACTIVITY_TIMEOUT_MINUTES", "20")
	defer os.Unsetenv("TURN_INACTIVITY_TIMEOUT_MINUTES")
	if timeout := getTurnTimeout(); timeout != 20*time.Minute {
		t.Errorf("Expected overridden turn timeout of 20m, got %v", timeout)
	}

	os.Setenv("TURN_HARD_DEADLINE_MINUTES", "60")
	defer os.Unsetenv("TURN_HARD_DEADLINE_MINUTES")
	if deadline := getHardTurnDeadline(); deadline != 60*time.Minute {
		t.Errorf("Expected overridden hard deadline of 60m, got %v", deadline)
	}
}

// TestTurnWatchdog_ActiveTurnNotKilledUnderHardDeadline verifies that a turn running
// for 20 minutes with fresh LastActivity (e.g. running tests, building, or awaiting CI)
// is NOT killed by the watchdog.
func TestTurnWatchdog_ActiveTurnNotKilledUnderHardDeadline(t *testing.T) {
	session := &AgySession{
		BotName:         "LongActiveBot",
		ActiveMessageID: 101,
		ActiveTurnStart: time.Now().Add(-20 * time.Minute), // Running for 20 minutes
		LastActivity:    time.Now().Add(-10 * time.Second), // Active 10 seconds ago!
		isAlive:         true,
	}

	timeout := 15 * time.Minute
	stalled := session.checkTurnInactivity(time.Now(), timeout)
	if stalled {
		t.Fatalf("Expected active turn (running for 20m with recent activity) NOT to be killed by watchdog")
	}

	session.mu.Lock()
	alive := session.isAlive
	activeID := session.ActiveMessageID
	session.mu.Unlock()

	if !alive {
		t.Errorf("Expected session to remain alive")
	}
	if activeID != 101 {
		t.Errorf("Expected ActiveMessageID to remain 101, got %d", activeID)
	}
}

// TestTurnWatchdog_HardDeadlineTriggeredWhenExceeded verifies that when a runaway turn
// exceeds the hard deadline (e.g. 45m), the watchdog terminates the process, salvages output,
// and reports the honest hard duration deadline rather than an inactivity error.
func TestTurnWatchdog_HardDeadlineTriggeredWhenExceeded(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	session := &AgySession{
		BotName:         "RunawayTurnBot",
		BotAPI:          bot,
		ChatID:          7788,
		ActiveMessageID: 202,
		ActiveTurnStart: time.Now().Add(-50 * time.Minute), // Exceeded 45m hard deadline
		LastActivity:    time.Now().Add(-1 * time.Minute),  // Recent activity
		TextBuffer:      "Analyzing step 999: infinite loop detected",
		isAlive:         true,
	}

	timeout := 15 * time.Minute
	hardDeadline := 45 * time.Minute
	stalled := session.checkTurnInactivity(time.Now(), timeout, hardDeadline)
	if !stalled {
		t.Fatalf("Expected runaway turn exceeding hard deadline to be terminated")
	}

	session.mu.Lock()
	alive := session.isAlive
	activeID := session.ActiveMessageID
	session.mu.Unlock()

	if alive {
		t.Errorf("Expected session.isAlive to be false after hard deadline kill")
	}
	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be reset to 0, got %d", activeID)
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	var foundText, foundHardDeadlineNotice bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "infinite loop detected") {
			foundText = true
		}
		if strings.Contains(unescaped, "Turn exceeded maximum duration deadline") {
			foundHardDeadlineNotice = true
		}
	}

	if !foundText {
		t.Errorf("Expected sent message to salvage text buffer, got: %v", sentBodies)
	}
	if !foundHardDeadlineNotice {
		t.Errorf("Expected sent message to report hard duration deadline, got: %v", sentBodies)
	}
}

func TestStreamRecovery_AutoFailoverToNextAccount(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := "#!/bin/sh\nexec cat\n"
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock agy: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	home1 := filepath.Join(tmpDir, "acc-stream-1")
	home2 := filepath.Join(tmpDir, "acc-stream-2")
	_ = os.MkdirAll(home1, 0755)
	_ = os.MkdirAll(home2, 0755)

	pool.accounts["acc-stream-1"] = &Account{
		ID:       "acc-stream-1",
		Email:    "stream1@example.com",
		HomeDir:  home1,
		State:    StateActive,
		LastUsed: time.Now(),
	}
	pool.accounts["acc-stream-2"] = &Account{
		ID:       "acc-stream-2",
		Email:    "stream2@example.com",
		HomeDir:  home2,
		State:    StateActive,
		LastUsed: time.Now().Add(-1 * time.Hour),
	}

	jsonl := `{"event":"result","result":{"status":"ERROR","error":"stream was interrupted"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "StreamFailoverBot",
		BotAPI:          bot,
		ChatID:          7777,
		UserID:          1001,
		ActiveMessageID: 555,
		TextBuffer:      "Partial generation before drop...",
		StreamRetries:   2, // Retries exhausted on acc-stream-1
		StdoutScanner:   scanner,
		UpdateChan:      make(chan struct{}, 10),
		AccountID:       "acc-stream-1",
		AccountHomeDir:  home1,
		Conversation:    "conv-stream-failover-777",
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()
	defer session.Kill()

	session.mu.Lock()
	rotatedAccID := session.AccountID
	rotatedHome := session.AccountHomeDir
	session.mu.Unlock()

	if rotatedAccID != "acc-stream-2" {
		t.Errorf("Expected session to failover to acc-stream-2, got: %s", rotatedAccID)
	}
	if rotatedHome != home2 {
		t.Errorf("Expected session home to switch to %s, got: %s", home2, rotatedHome)
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundFailoverNotice := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Stream Failover") && strings.Contains(unescaped, "acc-stream-2") {
			foundFailoverNotice = true
		}
	}
	if !foundFailoverNotice {
		t.Errorf("Expected Stream Failover notice to be sent to Telegram, got: %v", sentBodies)
	}
}

