package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
		if strings.Contains(decoded, "Сессия агента была остановлена или перезапущена") {
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
	session.readStdoutLoop(scanner, session.ctx)

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

	session.readStdoutLoop(scanner, session.ctx)

	// Result event clears text buffer
	session.mu.Lock()
	rem := session.TextBuffer
	session.mu.Unlock()

	if rem != "" {
		t.Errorf("Expected TextBuffer to be cleared on result, got len %d", len(rem))
	}
}
