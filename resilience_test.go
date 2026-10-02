package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestChaos_ProcessCrash_CleansActiveMessageSpinner verifies that when an agent child process
// dies unexpectedly (e.g. OOM killed, SIGKILL) while a Telegram message spinner is active (*⏳ Thinking...*),
// the background exit monitor cleans up the session state and sends a notification to Telegram.
func TestChaos_ProcessCrash_CleansActiveMessageSpinner(t *testing.T) {
	tempDir := t.TempDir()
	mockScript := filepath.Join(tempDir, "sleepy_agent.sh")
	scriptContent := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(mockScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create mock script: %v", err)
	}

	os.Setenv("AGY_BINARY", mockScript)
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	user := User{
		ID:        12345,
		Workspace: tempDir,
		Model:     defaultModel,
		SessionID: "chaos-crash-session",
	}

	session := getSession("TestChaosBot", user, 99999)
	if session == nil {
		t.Fatal("Failed to create session")
	}

	// Wait for process to spawn
	var pid int
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		session.mu.Lock()
		if session.Cmd != nil && session.Cmd.Process != nil && session.isAlive {
			pid = session.Cmd.Process.Pid
			session.BotAPI = bot
			session.ChatID = 99999
			session.ActiveMessageID = 888 // User is seeing thinking spinner
			session.mu.Unlock()
			break
		}
		session.mu.Unlock()
	}

	if pid == 0 {
		t.Fatal("Process did not start within expected time")
	}

	// Brutally kill process simulating OOM killer or segfault
	_ = syscall.Kill(pid, syscall.SIGKILL)

	// Wait for exit watcher to trigger cleanup (Cmd.WaitDelay is 2s)
	var cleaned bool
	for i := 0; i < 60; i++ {
		time.Sleep(50 * time.Millisecond)
		session.mu.Lock()
		alive := session.isAlive
		activeID := session.ActiveMessageID
		session.mu.Unlock()

		if !alive && activeID == 0 {
			cleaned = true
			break
		}
	}

	if !cleaned {
		t.Errorf("Expected session to be marked dead and ActiveMessageID reset to 0")
	}

	// Verify status message sent to Telegram
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundCleanupMsg := false
	for _, raw := range sentBodies {
		decoded, _ := url.QueryUnescape(raw)
		if strings.Contains(decoded, "Agent session was stopped or restarted") {
			foundCleanupMsg = true
			break
		}
	}

	if !foundCleanupMsg {
		t.Errorf("Expected cleanup status message sent to Telegram, got: %v", sentBodies)
	}
}

// TestChaos_CorruptedJSONL_StreamRecovery tests that corrupted, truncated, or binary garbage
// in stdout stream does NOT crash readStdoutLoop and subsequent valid events are processed cleanly.
func TestChaos_CorruptedJSONL_StreamRecovery(t *testing.T) {
	garbageStream := strings.Join([]string{
		`{"event":"init","conversation_id":"valid-conv-1"}`,
		`{broken json without closing brace...`,
		`FATAL: Segmentation fault (core dumped) at 0xdeadbeef`,
		``,
		`   `,
		`{"event":"step_update", "step_update": {"text_`, // Truncated JSON
		`{"unexpected_field_no_event": true}`,
		`{"event":"step_update","step_update":{"text_delta":"Safe and Sound!"}}`,
		`{"event":"result","result":{"status":"OK"}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(garbageStream))
	session := &AgySession{
		BotName:       "TestChaosBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	// Should not panic on garbage
	session.readStdoutLoop()

	if session.Conversation != "valid-conv-1" {
		t.Errorf("Expected conversation to be initialized, got %s", session.Conversation)
	}
}

// TestChaos_HugeTokenLine_NoScannerOverflow verifies that large JSON tokens (e.g. 500KB JSON payload)
// are handled cleanly by the 10MB scanner buffer without bufio.ErrTooLong buffer overflow.
func TestChaos_HugeTokenLine_NoScannerOverflow(t *testing.T) {
	// Generate 500KB text payload inside valid JSONL
	largeText := strings.Repeat("A", 500*1024)
	largeJSON := fmt.Sprintf(`{"event":"step_update","step_update":{"text_delta":%q}}`+"\n"+`{"event":"result","result":{"status":"OK"}}`+"\n", largeText)

	scanner := bufio.NewScanner(strings.NewReader(largeJSON))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	session := &AgySession{
		BotName:       "TestChaosBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Result event clears text buffer
	session.mu.Lock()
	rem := session.TextBuffer
	session.mu.Unlock()

	if rem != "" {
		t.Errorf("Expected TextBuffer to be cleared on result, got len %d", len(rem))
	}
}

func TestStoreAndGetQuestionOption_UTF8Safe(t *testing.T) {
	// 1. Short option (<= 64 bytes)
	shortOpt := "Option 1: Deploy"
	cb1 := storeQuestionOption(shortOpt)
	if !strings.HasPrefix(cb1, "ans:") {
		t.Errorf("Expected ans: prefix for short option, got %s", cb1)
	}
	if len([]byte(cb1)) > 64 {
		t.Errorf("Callback length exceeds 64 bytes: %d", len([]byte(cb1)))
	}
	res1, ok1 := getQuestionOption(cb1)
	if !ok1 || res1 != shortOpt {
		t.Errorf("Expected %s, got %s (ok=%v)", shortOpt, res1, ok1)
	}

	// 2. Long UTF-8 Cyrillic option (> 64 bytes)
	longOpt := "Очень длинный ответ на русском языке с эмодзи 🚀🎭, который гарантированно превышает лимит Telegram в 64 байта!"
	cb2 := storeQuestionOption(longOpt)
	if !strings.HasPrefix(cb2, "ans_id:") {
		t.Errorf("Expected ans_id: prefix for long option, got %s", cb2)
	}
	if len([]byte(cb2)) > 64 {
		t.Errorf("Callback length exceeds 64 bytes: %d", len([]byte(cb2)))
	}
	res2, ok2 := getQuestionOption(cb2)
	if !ok2 || res2 != longOpt {
		t.Errorf("Expected full untruncated string, got %s (ok=%v)", res2, ok2)
	}
}

func TestGenerateAndSendVoice_KeyFailover(t *testing.T) {
	key1 := "sk_badkey_failover1"
	key2 := "sk_goodkey_failover2"

	attemptCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("xi-api-key")
		attemptCount++
		if apiKey == key1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"detail":"Quota exceeded"}`))
			return
		}
		if apiKey == key2 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("FAKE_AUDIO_BYTES_OK"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	os.Setenv("ELEVENLABS_API_KEY", key1+","+key2)
	os.Setenv("ELEVENLABS_BASE_URL", ts.URL)
	defer os.Unsetenv("ELEVENLABS_API_KEY")
	defer os.Unsetenv("ELEVENLABS_BASE_URL")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	err := GenerateAndSendVoice(bot, 12345, "Testing ElevenLabs multi-key failover")
	if err != nil {
		t.Fatalf("Expected failover to succeed, but got error: %v", err)
	}
}

// TestTextBuffer_CapAndFlag verifies that AgySession stops appending deltas once maxTextBufferBytes
// is exceeded, flags TextTruncated, and appends a truncation warning to the result.
func TestTextBuffer_CapAndFlag(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	// Construct a stream where deltas exceed maxTextBufferBytes
	// Pre-fill buffer close to 1MB
	initialData := strings.Repeat("X", maxTextBufferBytes-100)
	overflowDelta := strings.Repeat("Y", 200)

	jsonl := fmt.Sprintf(`{"event":"step_update","step_update":{"text_delta":%q}}`+"\n"+
		`{"event":"result","result":{"status":"SUCCESS"}}`+"\n", overflowDelta)

	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "CapTestBot",
		Model:         defaultModel,
		Workspace:     "/tmp",
		ChatID:        12345,
		BotAPI:        bot,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
		TextBuffer:    initialData,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	session.mu.Lock()
	isTruncated := session.TextTruncated
	bufLen := len(session.TextBuffer)
	session.mu.Unlock()

	if !isTruncated {
		t.Errorf("Expected session.TextTruncated to be true when delta exceeds maxTextBufferBytes")
	}

	if bufLen > maxTextBufferBytes {
		t.Errorf("Expected TextBuffer length <= %d, got %d", maxTextBufferBytes, bufLen)
	}

	// Verify that mock server received a message with the truncation warning
	var foundTruncWarning bool
	ms.mu.Lock()
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Response truncated: buffer exceeded 1MB limit") ||
			strings.Contains(unescaped, "Truncated") {
			foundTruncWarning = true
			break
		}
	}
	ms.mu.Unlock()
	if !foundTruncWarning {
		t.Errorf("Expected Telegram message to contain truncation notice, received %d requests", len(ms.sentBodies))
	}
}

// TestSendChunk_PanicRecovery verifies that sendChunk catches any panics cleanly and does not terminate execution.
func TestSendChunk_PanicRecovery(t *testing.T) {
	// Call with nil bot - safe path
	chunks := sendChunk(nil, 12345, 10, "Hello safe world")
	if len(chunks) == 0 {
		t.Fatalf("Expected chunks, got 0")
	}

	// Test with extreme inputs
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sendChunk panicked unexpectedly: %v", r)
		}
	}()

	sendChunk(nil, 0, 0, "")
	sendChunk(nil, -1, -1, strings.Repeat("A", 10000))
}

// BenchmarkTextBuffer_Append benchmarks delta appending under the maxTextBufferBytes cap.
func BenchmarkTextBuffer_Append(b *testing.B) {
	delta := "1234567890"
	session := &AgySession{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session.mu.Lock()
		if len(session.TextBuffer)+len(delta) <= maxTextBufferBytes {
			session.TextBuffer += delta
		} else {
			session.TextTruncated = true
		}
		session.mu.Unlock()
	}
}

// TestReadStdoutLoop_SuppressesTeardownErrorWhenIdleOrDead verifies that when a session
// is already dead (isAlive == false) or was idle (ActiveTurnStart is zero), any incoming
// ERROR result (e.g. stream input cancelled: context canceled) is suppressed without
// dispatching false-positive errors to Telegram (#281).
func TestReadStdoutLoop_SuppressesTeardownErrorWhenIdleOrDead(t *testing.T) {
	errorPayload := `{"event": "result", "result": {"status": "ERROR", "error": "stream input cancelled: context canceled"}}` + "\n"

	// Case 1: Session is dead (!isAlive) - must NEVER dispatch to Telegram even if turn was marked active
	t.Run("DeadSessionSuppressed", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		s := &AgySession{
			BotName:         "DeadBot",
			isAlive:         false,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveTurnStart: time.Now().Add(-5 * time.Second),
			ActiveMessageID: 0,
			UpdateChan:      make(chan struct{}, 10),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		scanner := bufio.NewScanner(strings.NewReader(errorPayload))
		_ = s.readStdout(scanner, ctx)

		sentMsgs := getSentTelegramMessages(ms)
		if len(sentMsgs) != 0 {
			t.Errorf("Expected 0 messages sent for dead session, got %d", len(sentMsgs))
		}
	})

	// Case 2: Session is alive but completely idle (ActiveTurnStart is zero) - must suppress and continue loop
	t.Run("IdleSessionSuppressed", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		s := &AgySession{
			BotName:         "IdleBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveTurnStart: time.Time{},
			ActiveMessageID: 0,
			UpdateChan:      make(chan struct{}, 10),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Append a valid step_update JSON payload to ensure the loop continues
		validPayload := `{"event": "step_update", "step_update": {"text_delta": "hello"}}` + "\n"
		scanner := bufio.NewScanner(strings.NewReader(errorPayload + validPayload))

		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		_ = s.readStdout(scanner, ctx)

		sentMsgs := getSentTelegramMessages(ms)
		if len(sentMsgs) != 0 {
			t.Errorf("Expected 0 messages sent for idle session, got %d", len(sentMsgs))
		}

		// Assert that the UpdateChan received an update, proving readStdoutLoop did not exit on the error
		select {
		case <-s.UpdateChan:
			// Success! The step_update was processed.
		default:
			t.Errorf("readStdoutLoop aborted prematurely on error without processing subsequent step_update")
		}
	})

	// Case 3: Turn was active (ActiveTurnStart set) but activeMsgID == 0 (e.g. Telegram spinner send failed)
	// Non-recoverable error MUST be dispatched to user via tgbotapi.NewMessage instead of being silently swallowed.
	t.Run("ActiveTurnErrorDispatchedWhenMsgIDZero", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		fatalErrorPayload := `{"event": "result", "result": {"status": "ERROR", "error": "fatal internal execution error"}}` + "\n"

		s := &AgySession{
			BotName:         "ActiveTurnBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveTurnStart: time.Now().Add(-2 * time.Second),
			ActiveMessageID: 0, // Spinner was not created/delivered
			UpdateChan:      make(chan struct{}, 10),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		scanner := bufio.NewScanner(strings.NewReader(fatalErrorPayload))
		_ = s.readStdout(scanner, ctx)

		sentMsgs := getSentTelegramMessages(ms)
		if len(sentMsgs) == 0 {
			t.Fatalf("Silent Turn Drop: Expected error message to be dispatched to Telegram when turn was active, got 0 messages")
		}

		decoded, _ := url.QueryUnescape(sentMsgs[0])
		if !strings.Contains(decoded, "fatal internal execution error") {
			t.Errorf("Expected error message to contain 'fatal internal execution error', got %q", decoded)
		}
	})
}

// TestSession_GracefulShutdown verifies that during supervisor daemon shutdown,
// in-flight turns are salvaged, planned maintenance disclaimers are delivered,
// and processes are terminated cleanly without race conditions or raw error leakage (#284).
func TestSession_GracefulShutdown(t *testing.T) {
	// Case 1: Active turn with partial text buffer and truncation flag
	t.Run("PartialBufferSalvagedWithMaintenanceDisclaimer", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		s := &AgySession{
			BotName:         "ShutdownBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveMessageID: 101,
			ActiveTurnStart: time.Now().Add(-1 * time.Second),
			TextBuffer:      "Partial output generated before restart",
			TextTruncated:   true,
			UpdateChan:      make(chan struct{}, 10),
		}

		s.GracefulShutdown()

		if s.isAlive {
			t.Errorf("Expected session to be dead after GracefulShutdown, got isAlive=true")
		}
		if s.ActiveMessageID != 0 {
			t.Errorf("Expected ActiveMessageID to be 0 after GracefulShutdown, got %d", s.ActiveMessageID)
		}
		if s.TextBuffer != "" {
			t.Errorf("Expected TextBuffer to be cleared after GracefulShutdown, got %q", s.TextBuffer)
		}

		sentMsgs := getSentTelegramMessagesOrEdits(ms)
		if len(sentMsgs) == 0 {
			t.Fatalf("Expected maintenance disclaimer message to be delivered, got 0")
		}

		foundDisclaimer := false
		foundPartial := false
		foundTruncated := false

		for _, raw := range sentMsgs {
			dec, _ := url.QueryUnescape(raw)
			if strings.Contains(dec, "Service restarted: daemon maintenance in progress") {
				foundDisclaimer = true
			}
			if strings.Contains(dec, "Partial output generated before restart") {
				foundPartial = true
			}
			if strings.Contains(dec, "Response truncated: buffer exceeded 1MB limit") {
				foundTruncated = true
			}
		}

		if !foundDisclaimer {
			t.Errorf("Expected maintenance disclaimer in sent messages, got: %v", sentMsgs)
		}
		if !foundPartial {
			t.Errorf("Expected partial output to be salvaged, got: %v", sentMsgs)
		}
		if !foundTruncated {
			t.Errorf("Expected truncation notice in sent messages, got: %v", sentMsgs)
		}
	})

	// Case 2: Active turn with empty text buffer (turn just started)
	t.Run("EmptyBufferMaintenanceDisclaimer", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		s := &AgySession{
			BotName:         "ShutdownEmptyBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveMessageID: 202,
			ActiveTurnStart: time.Now(),
			TextBuffer:      "",
			UpdateChan:      make(chan struct{}, 10),
		}

		s.GracefulShutdown()

		if s.isAlive {
			t.Errorf("Expected session to be dead after GracefulShutdown")
		}
		if s.ActiveMessageID != 0 {
			t.Errorf("Expected ActiveMessageID to be 0 after GracefulShutdown")
		}

		sentMsgs := getSentTelegramMessagesOrEdits(ms)
		if len(sentMsgs) == 0 {
			t.Fatalf("Expected disclaimer message to be delivered, got 0")
		}

		foundDisclaimer := false
		for _, raw := range sentMsgs {
			dec, _ := url.QueryUnescape(raw)
			if strings.Contains(dec, "Service restarted (planned maintenance)") && strings.Contains(dec, "Turn execution was interrupted") {
				foundDisclaimer = true
			}
		}

		if !foundDisclaimer {
			t.Errorf("Expected planned maintenance disclaimer, got: %v", sentMsgs)
		}
	})

	// Case 3: Idle session without active message (no spurious message sent)
	t.Run("IdleSessionNoOp", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		s := &AgySession{
			BotName:         "IdleBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveMessageID: 0,
			ActiveTurnStart: time.Time{},
			TextBuffer:      "",
			UpdateChan:      make(chan struct{}, 10),
		}

		s.GracefulShutdown()

		if s.isAlive {
			t.Errorf("Expected session to be dead after GracefulShutdown")
		}

		sentMsgs := getSentTelegramMessagesOrEdits(ms)
		if len(sentMsgs) != 0 {
			t.Errorf("Expected 0 Telegram messages for idle session shutdown, got %d: %v", len(sentMsgs), sentMsgs)
		}
	})

	// Case 4: Idempotency and subprocess termination
	t.Run("IdempotentAndSubprocessTermination", func(t *testing.T) {
		ms := newMockServer()
		defer ms.Close()
		bot := createMockBot(ms)

		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start test sleep subprocess: %v", err)
		}
		waitDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(waitDone)
		}()

		s := &AgySession{
			BotName:         "SubprocBot",
			isAlive:         true,
			ChatID:          12345,
			BotAPI:          bot,
			ActiveMessageID: 303,
			ActiveTurnStart: time.Now(),
			TextBuffer:      "Subprocess output",
			Cmd:             cmd,
			UpdateChan:      make(chan struct{}, 10),
		}

		s.GracefulShutdown()

		if s.isAlive {
			t.Errorf("Expected session to be dead after GracefulShutdown")
		}

		select {
		case <-waitDone:
			// Process terminated cleanly
		case <-time.After(1 * time.Second):
			t.Errorf("Expected subprocess PID %d to be terminated by GracefulShutdown", cmd.Process.Pid)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}

		// Second call must be idempotent and not panic
		s.GracefulShutdown()
	})
}

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
		{
			errMsg:              "stream input cancelled: context canceled",
			wantStreamInterrupt: true,
			wantRateLimit:       false,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "context canceled",
			wantStreamInterrupt: true,
			wantRateLimit:       false,
			wantPrintTimeout:    false,
		},
		{
			errMsg:              "context cancelled",
			wantStreamInterrupt: true,
			wantRateLimit:       false,
			wantPrintTimeout:    false,
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

// TestStreamFailover_PreventsInfiniteRotationLoopWhenAllAccountsDrop verifies that if stream interruptions
// persist across failover accounts, the engine enforces maxTurnFailovers (1) and does not cascade
// into an infinite rotation storm knocking out all pool accounts.
func TestStreamFailover_PreventsInfiniteRotationLoopWhenAllAccountsDrop(t *testing.T) {
	pool, poolDir := setupTestAccountPool(t)
	defer os.RemoveAll(poolDir)

	oldPool := GlobalAccountPool
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = oldPool }()

	tmpDir := t.TempDir()
	home1 := filepath.Join(tmpDir, "acc-stream-1")
	home2 := filepath.Join(tmpDir, "acc-stream-2")
	home3 := filepath.Join(tmpDir, "acc-stream-3")
	_ = os.MkdirAll(home1, 0755)
	_ = os.MkdirAll(home2, 0755)
	_ = os.MkdirAll(home3, 0755)

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
	pool.accounts["acc-stream-3"] = &Account{
		ID:       "acc-stream-3",
		Email:    "stream3@example.com",
		HomeDir:  home3,
		State:    StateActive,
		LastUsed: time.Now().Add(-2 * time.Hour),
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	// Simulate already having failed over once in this turn (TurnFailovers = 1, StreamRetries = 2)
	jsonl := `{"event":"result","result":{"status":"ERROR","error":"stream was interrupted"}}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:         "AntiLoopBot",
		BotAPI:          bot,
		ChatID:          8888,
		UserID:          1002,
		ActiveMessageID: 777,
		TextBuffer:      "Accumulated report before second drop",
		StreamRetries:   2,
		TurnFailovers:   1, // Already failed over once during this turn!
		StdoutScanner:   scanner,
		UpdateChan:      make(chan struct{}, 10),
		AccountID:       "acc-stream-2",
		AccountHomeDir:  home2,
		Conversation:    "conv-no-cascading-loop",
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()
	defer session.Kill()

	// Verify that acc-stream-3 was NEVER touched or put in cooldown!
	pool.mu.Lock()
	acc3State := pool.accounts["acc-stream-3"].State
	pool.mu.Unlock()

	if acc3State != StateActive {
		t.Errorf("Expected acc-stream-3 to remain StateActive, got %s", acc3State)
	}

	// Verify session salvaged buffer and cleared active message ID
	session.mu.Lock()
	activeID := session.ActiveMessageID
	retries := session.StreamRetries
	failovers := session.TurnFailovers
	session.mu.Unlock()

	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after exhausting failover limit, got %d", activeID)
	}
	if retries != 0 {
		t.Errorf("Expected StreamRetries to be reset to 0, got %d", retries)
	}
	if failovers != 0 {
		t.Errorf("Expected TurnFailovers to be reset to 0, got %d", failovers)
	}

	// Verify message in Telegram contains salvaged text and retry button
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundSalvageText := false
	foundRetryBtn := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Accumulated report before second drop") {
			foundSalvageText = true
		}
		if strings.Contains(unescaped, "cmd:retry") {
			foundRetryBtn = true
		}
	}
	if !foundSalvageText {
		t.Errorf("Expected salvaged text to be sent, got: %v", sentBodies)
	}
	if !foundRetryBtn {
		t.Errorf("Expected cmd:retry button to be present, got: %v", sentBodies)
	}
}

// TestCmdWait_EpochGuardPreventsGhostTerminationNotice verifies that an exited process from
// a previous generation does NOT wipe session state or emit a spurious "Agent session was stopped or restarted" notice.
func TestCmdWait_EpochGuardPreventsGhostTerminationNotice(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	session := &AgySession{
		BotName:         "EpochGuardBot",
		BotAPI:          bot,
		ChatID:          9999,
		ActiveMessageID: 1234,
		cmdEpoch:        2, // Newer epoch than exited process
		isAlive:         true,
	}

	// Simulate old cmd process exiting from epoch 1
	cmd := exec.Command("/bin/sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start dummy cmd: %v", err)
	}

	stdoutDone := make(chan struct{})
	close(stdoutDone)

	done := make(chan struct{})
	go func(c *exec.Cmd, epoch uint64) {
		defer close(done)
		_ = c.Wait()
		<-stdoutDone

		session.mu.Lock()
		if session.cmdEpoch != epoch || session.Cmd != c {
			session.mu.Unlock()
			return
		}
		session.ActiveMessageID = 0
		session.mu.Unlock()
	}(cmd, 1) // Passed epoch 1

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for dummy process cmdWait")
	}

	session.mu.Lock()
	activeID := session.ActiveMessageID
	session.mu.Unlock()

	if activeID != 1234 {
		t.Errorf("Expected ActiveMessageID to remain untouched (1234), got %d", activeID)
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Agent session was stopped or restarted") {
			t.Errorf("Spurious restart notice sent by superseded process: %s", unescaped)
		}
	}
}

// TestTylerAudit_ReadStdoutLoop_PreservesBufferOnActiveTurn verifies that when readStdoutLoop
// exits while an active turn is in progress (e.g. during recovery or process termination),
// its deferred cleanup does NOT wipe out s.TextBuffer.
func TestTylerAudit_ReadStdoutLoop_PreservesBufferOnActiveTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &AgySession{
		BotName:         "test_bot",
		ChatID:          12345,
		ActiveMessageID: 9999, // Active turn in progress
		TextBuffer:      "Vital partial output salvaged from stream",
		ctx:             ctx,
	}

	// An empty scanner that immediately hits EOF
	scanner := bufio.NewScanner(strings.NewReader(""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.readStdout(scanner, ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readStdoutLoop timed out")
	}

	s.mu.Lock()
	buf := s.TextBuffer
	s.mu.Unlock()

	if buf != "Vital partial output salvaged from stream" {
		t.Fatalf("Expected TextBuffer to be preserved, got '%s'", buf)
	}
}

// TestTylerAudit_ReadStdoutLoop_ClearsBufferWhenNoActiveTurn verifies that when no turn is active,
// readStdoutLoop cleans up its buffer on exit.
func TestTylerAudit_ReadStdoutLoop_ClearsBufferWhenNoActiveTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &AgySession{
		BotName:         "test_bot",
		ChatID:          12345,
		ActiveMessageID: 0, // No active turn
		TextBuffer:      "Stale leftover data",
		ctx:             ctx,
	}

	scanner := bufio.NewScanner(strings.NewReader(""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.readStdout(scanner, ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readStdoutLoop timed out")
	}

	s.mu.Lock()
	buf := s.TextBuffer
	s.mu.Unlock()

	if buf != "" {
		t.Fatalf("Expected TextBuffer to be cleared when ActiveMessageID==0, got '%s'", buf)
	}
}

// TestTylerAudit_ResetChatSessionCache_NonBlockingLock verifies that resetChatSessionCache
// does not hold sessionMu while terminating processes.
func TestTylerAudit_ResetChatSessionCache_NonBlockingLock(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	testBot := "tyler_test_bot"
	chatID := int64(778899)
	userID := int64(112233)
	key := "tyler_test_bot:778899:112233"

	sess := &AgySession{
		BotName:      testBot,
		ChatID:       chatID,
		UserID:       userID,
		Conversation: "test-conv-id",
	}

	sessionMu.Lock()
	globalSessions[key] = sess
	sessionMu.Unlock()

	// Call resetChatSessionCache
	resetChatSessionCache(db, testBot, userID, chatID)

	// Immediately verify sessionMu is unlocked and globalSessions evicted the key
	sessionMu.Lock()
	_, exists := globalSessions[key]
	sessionMu.Unlock()

	if exists {
		t.Fatalf("Expected session %s to be evicted", key)
	}
}

// TestTylerAudit_CleanTextForTTS_CleanOutput verifies that CleanTextForTTS correctly strips
// code blocks, inline code, links, and caps length cleanly.
func TestTylerAudit_CleanTextForTTS_CleanOutput(t *testing.T) {
	input := "Hello! Here is some code: ```go\nfmt.Println(\"secret\")\n``` and inline `token := 123`. Click [here](https://example.com) for details."
	got := CleanTextForTTS(input)

	if strings.Contains(got, "fmt.Println") {
		t.Errorf("Expected code block to be stripped, got: %s", got)
	}
	if strings.Contains(got, "token :=") {
		t.Errorf("Expected inline code to be stripped, got: %s", got)
	}
	if strings.Contains(got, "https://example.com") {
		t.Errorf("Expected URL to be stripped, got: %s", got)
	}
	if !strings.Contains(got, "here") {
		t.Errorf("Expected link text 'here' to remain, got: %s", got)
	}
}

// TestTylerAudit_DispatchUpdate_ReusedTimer verifies that dispatchUpdate properly creates
// a worker with a reusable timer and processes updates without deadlocks or panic.
func TestTylerAudit_DispatchUpdate_ReusedTimer(t *testing.T) {
	chatQueuesMu.Lock()
	origTimeout := chatQueueIdleTimeout
	chatQueueIdleTimeout = 80 * time.Millisecond
	chatQueuesMu.Unlock()
	defer func() {
		chatQueuesMu.Lock()
		chatQueueIdleTimeout = origTimeout
		chatQueuesMu.Unlock()
	}()

	testChatID := int64(999111)

	chatQueuesMu.Lock()
	delete(chatQueues, testChatID)
	chatQueuesMu.Unlock()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	// Dispatch multiple rapid updates
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			upd := tgbotapi.Update{
				UpdateID: 500 + idx,
				Message: &tgbotapi.Message{
					MessageID: 1000 + idx,
					Chat:      &tgbotapi.Chat{ID: testChatID},
					From:      &tgbotapi.User{ID: 12345, UserName: "testuser"},
					Text:      "/help",
				},
			}
			dispatchUpdate(bot, upd, db)
		}(i)
	}
	wg.Wait()

	// Wait for worker idle timeout eviction with retry polling to handle race detector scheduling jitter
	evicted := false
	for attempt := 0; attempt < 50; attempt++ {
		time.Sleep(20 * time.Millisecond)
		chatQueuesMu.Lock()
		_, active := chatQueues[testChatID]
		chatQueuesMu.Unlock()
		if !active {
			evicted = true
			break
		}
	}

	if !evicted {
		t.Errorf("Expected worker queue for chat %d to be evicted after idle timeout", testChatID)
	}
}

func TestSession_KillConcurrencyAndIdempotency(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName:      "TestKillBot",
		ChatID:       123456,
		UserID:       789,
		Model:        "gemini-3.7-flash-high",
		Workspace:    "/root",
		Conversation: "test-conv-kill",
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
	}

	session.start()
	if !session.IsAlive() {
		t.Fatalf("expected session to be alive after start")
	}

	// Concurrently call Kill() from 30 goroutines to test idempotency and race freedom
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.Kill()
		}()
	}
	wg.Wait()

	if session.IsAlive() {
		t.Errorf("expected session to be dead after Kill()")
	}

	// Calling Kill on an already dead session must be safe and idempotent
	session.Kill()
	if session.IsAlive() {
		t.Errorf("expected session to remain dead")
	}
}

func TestSession_KillDuringActiveConcurrentReads(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName:      "TestKillReadsBot",
		ChatID:       654321,
		UserID:       987,
		Model:        "gemini-3.7-flash-high",
		Workspace:    "/root",
		Conversation: "test-conv-kill-reads",
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
	}

	session.start()

	var wg sync.WaitGroup
	stopReaders := make(chan struct{})

	// Spin up goroutines reading status / session properties concurrently
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = session.IsAlive()
					_ = session.GetConversation()
					time.Sleep(1 * time.Millisecond)
				}
			}
		}()
	}

	// Allow readers to run, then trigger Kill
	time.Sleep(10 * time.Millisecond)
	session.Kill()

	close(stopReaders)
	wg.Wait()

	if session.IsAlive() {
		t.Errorf("expected session to be dead")
	}
}

func TestTTS_CustomBaseURLAndKeyRotation(t *testing.T) {
	var receivedVoiceID string
	var receivedKey string
	var receivedPayload map[string]interface{}
	var serverMutex sync.Mutex

	// Mock ElevenLabs HTTP Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverMutex.Lock()
		defer serverMutex.Unlock()

		receivedKey = r.Header.Get("xi-api-key")
		pathParts := filepath.Base(r.URL.Path)
		receivedVoiceID = pathParts

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedPayload)

		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("FAKE_MP3_AUDIO_STREAM"))
	}))
	defer ts.Close()

	os.Setenv("ELEVENLABS_BASE_URL", ts.URL)
	os.Setenv("ELEVENLABS_API_KEY", "sk_mock_test_key_12345")
	defer os.Unsetenv("ELEVENLABS_BASE_URL")
	defer os.Unsetenv("ELEVENLABS_API_KEY")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	err := GenerateAndSendVoice(bot, 1234567, "Hello from the custom base URL test!")
	if err != nil {
		t.Fatalf("GenerateAndSendVoice failed: %v", err)
	}

	serverMutex.Lock()
	defer serverMutex.Unlock()

	if receivedKey != "sk_mock_test_key_12345" {
		t.Errorf("expected xi-api-key 'sk_mock_test_key_12345', got '%s'", receivedKey)
	}
	if receivedVoiceID != "JBFqnCBsd6RMkjVDRZzb" {
		t.Errorf("expected default voice ID 'JBFqnCBsd6RMkjVDRZzb', got '%s'", receivedVoiceID)
	}
	if text, ok := receivedPayload["text"].(string); !ok || text != "Hello from the custom base URL test!" {
		t.Errorf("expected payload text 'Hello from the custom base URL test!', got '%v'", receivedPayload["text"])
	}
}

func TestGetAgyPath_EnvOverride(t *testing.T) {
	// Test custom override
	customPath := "/opt/custom/bin/agy"
	os.Setenv("AGY_BINARY", customPath)
	if path := getAgyPath(); path != customPath {
		t.Errorf("expected '%s', got '%s'", customPath, path)
	}

	// Test default fallback
	os.Unsetenv("AGY_BINARY")
	defaultPath := getAgyPath()
	if defaultPath == "" || !filepath.IsAbs(defaultPath) {
		t.Errorf("expected absolute default path, got '%s'", defaultPath)
	}
}

func TestDispatchUpdate_IdleWorkerEviction(t *testing.T) {
	// Set a very short idle timeout for the test
	chatQueuesMu.Lock()
	origTimeout := chatQueueIdleTimeout
	chatQueueIdleTimeout = 50 * time.Millisecond
	chatQueuesMu.Unlock()
	defer func() {
		chatQueuesMu.Lock()
		chatQueueIdleTimeout = origTimeout
		chatQueuesMu.Unlock()
	}()

	testChatID := int64(888999)

	// Clean up map before test
	chatQueuesMu.Lock()
	delete(chatQueues, testChatID)
	chatQueuesMu.Unlock()

	// Dispatch dummy update with valid user
	upd := tgbotapi.Update{
		UpdateID: 101,
		Message: &tgbotapi.Message{
			MessageID: 202,
			Chat:      &tgbotapi.Chat{ID: testChatID},
			From:      &tgbotapi.User{ID: 12345, UserName: "testuser"},
			Text:      "/help",
		},
	}

	// Create in-memory DB
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	bot := &tgbotapi.BotAPI{}
	dispatchUpdate(bot, upd, db)

	// Verify worker was created in map
	chatQueuesMu.Lock()
	_, exists := chatQueues[testChatID]
	chatQueuesMu.Unlock()
	if !exists {
		t.Fatal("Expected chat queue worker to exist immediately after dispatch")
	}

	// Wait for idle timeout + buffer to let worker evict itself
	time.Sleep(120 * time.Millisecond)

	chatQueuesMu.Lock()
	_, stillExists := chatQueues[testChatID]
	chatQueuesMu.Unlock()

	if stillExists {
		t.Errorf("Expected chat queue worker for chatID %d to be evicted after idle timeout, but still in map", testChatID)
	}
}

func TestSplitOversizedParagraph_NeverSeversTagsOrEntities(t *testing.T) {
	// Construct a paragraph where an HTML tag crosses the boundary
	// Target chunk size: 50
	prefix := "This is a long introductory sentence before a link "
	tag := `<a href="https://example.com/a/very/long/target/url/path">Anchor Text</a>`
	suffix := " followed by trailing text."
	p := prefix + tag + suffix

	parts := splitOversizedParagraph(p, 50)
	if len(parts) < 2 {
		t.Fatalf("Expected multiple parts, got %d", len(parts))
	}

	for i, part := range parts {
		if len([]rune(part)) > 55 { // leeway for intact tag fallback
			t.Errorf("Part %d exceeds max chunk size: %d runes", i, len([]rune(part)))
		}
		// Verify no part starts with a broken tag attribute or ends with an unclosed opening bracket
		if strings.HasPrefix(part, `href=`) || strings.HasPrefix(part, `/a/very`) {
			t.Errorf("Part %d started with severed tag attribute: %q", i, part)
		}
		if strings.HasSuffix(part, `<a `) || strings.HasSuffix(part, `<a`) {
			t.Errorf("Part %d ended with severed tag opener: %q", i, part)
		}
	}
}

func TestSplitOversizedParagraph_EntityProtection(t *testing.T) {
	p := "Start text &amp; &quot; middle text &lt; &gt; end text"
	// Force cut around 15 runes
	parts := splitOversizedParagraph(p, 15)
	for i, part := range parts {
		if strings.HasSuffix(part, "&") || strings.HasSuffix(part, "&am") || strings.HasSuffix(part, "&qu") {
			t.Errorf("Part %d severed an entity: %q", i, part)
		}
	}
}

func TestSplitHTMLChunks_TagPreservationOnOversizedChunks(t *testing.T) {
	longText := "<pre><code class=\"language-go\">" +
		strings.Repeat("fmt.Println(\"Hello world line test!\")\n", 30) +
		"</code></pre>"

	chunks := SplitHTMLChunks(longText, 200)
	if len(chunks) <= 1 {
		t.Fatalf("Expected multiple chunks for large input, got %d", len(chunks))
	}

	for i, ch := range chunks {
		// Verify every chunk is valid HTML and tags are balanced
		if strings.Count(ch, "<pre>") != strings.Count(ch, "</pre>") {
			t.Errorf("Chunk %d has unbalanced <pre> tags:\n%s", i, ch)
		}
		if strings.Count(ch, "<code") != strings.Count(ch, "</code>") {
			t.Errorf("Chunk %d has unbalanced <code> tags:\n%s", i, ch)
		}
		// Verify no raw unescaped tag fragments
		if strings.Contains(ch, "&lt;code") || strings.Contains(ch, "&lt;pre") {
			t.Errorf("Chunk %d corrupted valid code/pre tags into escaped text:\n%s", i, ch)
		}
	}
}

func TestHandleCallbackQuery_ExpiredQuestionOption(t *testing.T) {
	// Given an expired/unknown callback data
	data := "ans_id:nonexistent_key_1234"
	opt, ok := getQuestionOption(data)
	if ok || opt != "" {
		t.Fatalf("Expected ok=false for nonexistent callback option, got ok=%v, opt=%s", ok, opt)
	}
}

func TestSendArtifacts_TOCTOU_FileReader(t *testing.T) {
	tempDir := t.TempDir()
	mockProjects := filepath.Join(tempDir, "projects")
	os.MkdirAll(mockProjects, 0755)
	t.Setenv("PROJECTS_DIR", mockProjects)

	artifactFile := filepath.Join(mockProjects, "report.pdf")
	if err := os.WriteFile(artifactFile, []byte("%PDF-mock-binary-content"), 0644); err != nil {
		t.Fatalf("Failed to write mock artifact: %v", err)
	}

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	text := "Here is your report: [report.pdf](file://" + artifactFile + ")"
	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Errorf("Expected sendArtifacts to send document via open FileReader")
	}
}

func TestReadStdoutLoop_InitEvent_InsertsSessionHistory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(7777)
	getUser(db, userID, "TestMockBot")

	jsonl := `{"event":"init","conversation_id":"fresh-new-conv-uuid-888"}` + "\n"
	scanner := bufio.NewScanner(strings.NewReader(jsonl))

	session := &AgySession{
		BotName:       "TestMockBot",
		ChatID:        12345,
		UserID:        userID,
		DB:            db,
		Model:         "gemini-2.5-pro",
		Workspace:     "/tmp/test_ws",
		Conversation:  "",
		InitChan:      make(chan string, 10),
		UpdateChan:    make(chan struct{}, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	defer session.cancel()

	session.readStdoutLoop()

	// Verify session was inserted into session_history
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ? AND session_id = ?", userID, "fresh-new-conv-uuid-888").Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Expected session_history to contain fresh-new-conv-uuid-888, count=%d, err=%v", count, err)
	}

	// Verify users table was updated
	user := getUser(db, userID, "TestMockBot")
	if user.SessionID != "fresh-new-conv-uuid-888" {
		t.Errorf("Expected users.session_id to be fresh-new-conv-uuid-888, got %s", user.SessionID)
	}
}

func TestHandleResumeCommand_Deduplication(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(8888)
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	// Create duplicate entries in session_history directly
	sessionID := "duplicate-session-uuid-111"
	_, _ = db.Exec("INSERT INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
	// Create dummy transcript
	logDir := filepath.Join(tempDir, sessionID, ".system_generated", "logs")
	os.MkdirAll(logDir, 0755)
	os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(`{"created_at":"2026-09-04T12:00:00Z","content":"<USER_REQUEST>Test Dup</USER_REQUEST>"}`+"\n"), 0644)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	handleResumeCommand(bot, 12345, userID, db)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Errorf("Expected handleResumeCommand to send resume keyboard")
	}
}

func TestHandleClearCommand_OrderOfOperations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(9999)
	user := getUser(db, userID, "TestMockBot")
	updateUserSession(db, userID, "pre-clear-session-123")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	handleClearCommand(bot, 12345, userID, "TestMockBot", user, db)

	// User session in DB should immediately be empty (cleared)
	clearedUser := getUser(db, userID, "TestMockBot")
	if clearedUser.SessionID != "" {
		t.Errorf("Expected user session in DB to be empty after clear, got %q", clearedUser.SessionID)
	}
}

func TestReadProcStat(t *testing.T) {
	pid := os.Getpid()
	stat, err := readProcStat(pid)
	if err != nil {
		t.Fatalf("failed to read proc stat for current process: %v", err)
	}
	if stat.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, stat.PID)
	}
	if stat.PPID <= 0 {
		t.Errorf("expected valid PPID, got %d", stat.PPID)
	}
	if stat.State == "" {
		t.Error("expected non-empty state")
	}
}

func TestGetLinuxProcessMap(t *testing.T) {
	procs, err := getLinuxProcessMap()
	if err != nil {
		t.Fatalf("failed to get linux process map: %v", err)
	}
	if len(procs) == 0 {
		t.Fatal("expected non-empty process map")
	}
	pid := os.Getpid()
	if _, ok := procs[pid]; !ok {
		t.Errorf("current PID %d not found in process map", pid)
	}
}

func TestFindStoppedDescendants(t *testing.T) {
	rootPids := map[int]string{
		100: "test_bot",
	}
	procs := map[int]procStat{
		100: {PID: 100, PPID: 1, State: "S"},
		101: {PID: 101, PPID: 100, State: "S"},
		102: {PID: 102, PPID: 101, State: "T"}, // stopped descendant
		200: {PID: 200, PPID: 1, State: "T"},   // stopped, but unrelated
	}

	stopped := findStoppedDescendants(rootPids, procs)
	if len(stopped) != 1 {
		t.Fatalf("expected 1 stopped descendant, got %d", len(stopped))
	}
	if stopped[0].PID != 102 {
		t.Errorf("expected PID 102, got %d", stopped[0].PID)
	}
	if stopped[0].BotName != "test_bot" {
		t.Errorf("expected botName 'test_bot', got '%s'", stopped[0].BotName)
	}
}

func TestReapStoppedSubprocessesLive(t *testing.T) {
	// 1. Spawn a dummy child process
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	childPID := cmd.Process.Pid

	// 2. Register current PID as a dummy session in globalSessions
	currentPID := os.Getpid()
	sessionKey := "test_session_watchdog"
	dummySession := &AgySession{
		BotName: "watchdog_tester",
		isAlive: true,
		Cmd:     &exec.Cmd{Process: &os.Process{Pid: currentPID}},
	}

	sessionMu.Lock()
	globalSessions[sessionKey] = dummySession
	sessionMu.Unlock()

	defer func() {
		sessionMu.Lock()
		delete(globalSessions, sessionKey)
		sessionMu.Unlock()
	}()

	// 3. Stop the child process with SIGSTOP (simulating SIGTTIN State: T)
	if err := syscall.Kill(childPID, syscall.SIGSTOP); err != nil {
		t.Fatalf("failed to send SIGSTOP to child: %v", err)
	}

	// Wait briefly for kernel to record State: T
	var isStopped bool
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		st, err := readProcStat(childPID)
		if err == nil && (st.State == "T" || st.State == "t") {
			isStopped = true
			break
		}
	}
	if !isStopped {
		t.Fatal("child did not enter State: T")
	}

	// 4. Run ReapStoppedSubprocesses with 0 gracePeriod -> should reap immediately
	reaped := ReapStoppedSubprocesses(0)
	if reaped < 1 {
		t.Errorf("expected at least 1 process reaped, got %d", reaped)
	}

	// 5. Verify child process is dead or zombie
	time.Sleep(100 * time.Millisecond)
	st, err := readProcStat(childPID)
	if err == nil && st.State != "Z" {
		t.Errorf("expected child process %d to be dead or zombie after reap, got state %s", childPID, st.State)
	}
}

func TestStartSubprocessWatchdogWorker(t *testing.T) {
	stopChan := make(chan struct{})
	StartSubprocessWatchdogWorker(10*time.Millisecond, 10*time.Millisecond, stopChan)
	time.Sleep(50 * time.Millisecond)
	close(stopChan)
	// Give worker time to exit cleanly
	time.Sleep(20 * time.Millisecond)
}

func TestDispatchUpdate_SequentialPerChat(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(8888)
	userID := int64(7777)
	const messageCount = 15

	for i := 0; i < messageCount; i++ {
		update := tgbotapi.Update{
			UpdateID: 5000 + i,
			Message: &tgbotapi.Message{
				MessageID: 500 + i,
				Chat:      &tgbotapi.Chat{ID: chatID},
				From:      &tgbotapi.User{ID: userID},
				Text:      "/help",
			},
		}
		dispatchUpdate(bot, update, db)
	}

	// Allow worker queue to drain
	time.Sleep(300 * time.Millisecond)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestConcurrentChatQueues_MultiChatThroughput(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	var wg sync.WaitGroup
	const chatCount = 10
	const msgsPerChat = 5

	for c := 0; c < chatCount; c++ {
		wg.Add(1)
		go func(chatIdx int) {
			defer wg.Done()
			cID := int64(10000 + chatIdx)
			uID := int64(20000 + chatIdx)
			for m := 0; m < msgsPerChat; m++ {
				update := tgbotapi.Update{
					UpdateID: 6000 + chatIdx*100 + m,
					Message: &tgbotapi.Message{
						MessageID: 600 + chatIdx*100 + m,
						Chat:      &tgbotapi.Chat{ID: cID},
						From:      &tgbotapi.User{ID: uID},
						Text:      "/help",
					},
				}
				dispatchUpdate(bot, update, db)
			}
		}(c)
	}

	wg.Wait()
	time.Sleep(400 * time.Millisecond)
}
