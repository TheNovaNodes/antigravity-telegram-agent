package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

// TestFallbackModel_UnknownCurrent_NoThrash verifies fallbackChain integrity for standard and non-standard models.
func TestFallbackModel_UnknownCurrent_NoThrash(t *testing.T) {
	// Non-chain models must fail over to flagship model (fallbackChain[0])
	unknowns := []string{"claude-3-7-sonnet", "gpt-4o", "mistral-large", "custom-llm"}
	for _, u := range unknowns {
		fb := getFallbackModel(u)
		if fb != fallbackChain[0] {
			t.Errorf("getFallbackModel(%q) = %q; want flagship %q", u, fb, fallbackChain[0])
		}
	}

	// Flagship model must fail over to second entry
	if fb := getFallbackModel(fallbackChain[0]); fb != fallbackChain[1] {
		t.Errorf("getFallbackModel(%q) = %q; want %q", fallbackChain[0], fb, fallbackChain[1])
	}

	// Terminal model must return empty string
	terminal := fallbackChain[len(fallbackChain)-1]
	if fb := getFallbackModel(terminal); fb != "" {
		t.Errorf("getFallbackModel(%q) = %q; want empty string (exhaustion)", terminal, fb)
	}

	// Exhaustion verification: chaining until terminal must terminate cleanly
	curr := "unknown-entry"
	visited := make(map[string]bool)
	steps := 0
	for {
		next := getFallbackModel(curr)
		if next == "" {
			break
		}
		if visited[next] {
			t.Fatalf("Cycle detected in fallback chain: model %q visited twice", next)
		}
		visited[next] = true
		curr = next
		steps++
		if steps > len(fallbackChain)+2 {
			t.Fatalf("Fallback chain exceeded expected depth without terminating")
		}
	}
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

func getSentTelegramMessages(ms *mockServer) []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var msgs []string
	for i, req := range ms.sentRequests {
		if strings.Contains(req.URL.Path, "sendMessage") {
			msgs = append(msgs, ms.sentBodies[i])
		}
	}
	return msgs
}

func getSentTelegramMessagesOrEdits(ms *mockServer) []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var msgs []string
	for i, req := range ms.sentRequests {
		if strings.Contains(req.URL.Path, "sendMessage") || strings.Contains(req.URL.Path, "editMessageText") {
			msgs = append(msgs, ms.sentBodies[i])
		}
	}
	return msgs
}

func getAllSentTelegramBodies(ms *mockServer) []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var msgs []string
	for _, b := range ms.sentBodies {
		msgs = append(msgs, b)
	}
	return msgs
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
		s.readStdoutLoop(scanner, ctx)

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

		s.readStdoutLoop(scanner, ctx)

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
		s.readStdoutLoop(scanner, ctx)

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
