package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSession_ProcessLifecycleAndPipesClosing(t *testing.T) {
	tempDir := t.TempDir()
	mockScript := filepath.Join(tempDir, "mock_agy.sh")
	scriptContent := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(mockScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create mock script: %v", err)
	}

	os.Setenv("AGY_BINARY", mockScript)
	defer os.Unsetenv("AGY_BINARY")

	user := User{
		ID:        999,
		Workspace: tempDir,
		Model:     "gemini-3.7-flash-high",
		SessionID: "test-lifecycle-conv",
	}

	session := getSession("TestLifecycleBot", user, 12345)
	if session == nil {
		t.Fatal("Expected session to be created")
	}

	// Allow process to start
	time.Sleep(100 * time.Millisecond)

	session.mu.Lock()
	alive := session.isAlive
	hasCmd := session.Cmd != nil
	hasStdin := session.Stdin != nil
	hasStdout := session.StdoutPipe != nil
	waitDelay := time.Duration(0)
	if session.Cmd != nil {
		waitDelay = session.Cmd.WaitDelay
	}
	session.mu.Unlock()

	if !alive || !hasCmd || !hasStdin || !hasStdout {
		t.Fatalf("Session process state incomplete: alive=%v cmd=%v stdin=%v stdout=%v", alive, hasCmd, hasStdin, hasStdout)
	}

	if waitDelay != 2*time.Second {
		t.Errorf("Expected Cmd.WaitDelay to be 2s, got %v", waitDelay)
	}

	// Kill session
	session.Kill()

	session.mu.Lock()
	afterAlive := session.isAlive
	afterCmd := session.Cmd
	afterStdin := session.Stdin
	afterStdout := session.StdoutPipe
	afterActiveMsgID := session.ActiveMessageID
	afterTextBuffer := session.TextBuffer
	session.mu.Unlock()

	if afterAlive {
		t.Errorf("Expected session to not be alive after Kill()")
	}
	if afterCmd != nil || afterStdin != nil || afterStdout != nil {
		t.Errorf("Expected handles to be nil after Kill(), got cmd=%v stdin=%v stdout=%v", afterCmd, afterStdin, afterStdout)
	}
	if afterActiveMsgID != 0 || afterTextBuffer != "" {
		t.Errorf("Expected buffer and activeMsgID reset, got ID=%d text=%q", afterActiveMsgID, afterTextBuffer)
	}
}
