package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"strings"
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

// TestReadStdoutLoop_SuppressesTeardownErrorWhenIdleOrDead verifies that when a session
// is already dead (isAlive == false) or was idle (ActiveTurnStart is zero), any incoming
// ERROR result (e.g. stream input cancelled: context canceled) is suppressed without
// dispatching false-positive errors to Telegram (#281).
func TestReadStdoutLoop_SuppressesTeardownErrorWhenIdleOrDead(t *testing.T) {
	errorPayload := `{"event": "result", "result": {"status": "ERROR", "error": "stream input cancelled: context canceled"}}` + "\n"

	// Case 1: Session is dead (!isAlive)
	t.Run("DeadSessionSuppressed", func(t *testing.T) {
		s := &AgySession{
			BotName:         "DeadBot",
			isAlive:         false,
			ActiveTurnStart: time.Time{},
			ActiveMessageID: 0,
			UpdateChan:      make(chan struct{}, 10),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		scanner := bufio.NewScanner(strings.NewReader(errorPayload))
		// Should return immediately without panics or sending to nil BotAPI
		s.readStdoutLoop(scanner, ctx)
	})

	// Case 2: Session is alive but completely idle (ActiveTurnStart is zero)
	t.Run("IdleSessionSuppressed", func(t *testing.T) {
		s := &AgySession{
			BotName:         "IdleBot",
			isAlive:         true,
			ActiveTurnStart: time.Time{},
			ActiveMessageID: 0,
			UpdateChan:      make(chan struct{}, 10),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Append a valid step_update JSON payload to ensure the loop continues
		validPayload := `{"event": "step_update", "step_update": {"text_delta": "hello"}}` + "\n"
		scanner := bufio.NewScanner(strings.NewReader(errorPayload + validPayload))

		// Run loop (will read error, continue, read step_update, and block on context)
		// We'll close context asynchronously to allow readStdoutLoop to finish reading lines
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		s.readStdoutLoop(scanner, ctx)

		// Assert that the UpdateChan received an update, proving readStdoutLoop did not exit on the error
		select {
		case <-s.UpdateChan:
			// Success! The step_update was processed.
		default:
			t.Errorf("readStdoutLoop aborted prematurely on error without processing subsequent step_update")
		}
	})
}
