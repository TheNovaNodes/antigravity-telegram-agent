package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestGoleak_SessionStdoutLoop_CleanTermination(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	mockData := strings.Join([]string{
		`{"event":"init","init":{"conversation_id":"leak-test-conv"}}`,
		`{"event":"step_update","step_update":{"text_delta":"Streaming data..."}}`,
		`{"event":"result","result":{"status":"OK"}}`,
	}, "\n") + "\n"

	scanner := bufio.NewScanner(strings.NewReader(mockData))
	session := &AgySession{
		BotName:       "LeakTestBot",
		Model:         defaultModel,
		Workspace:     "/tmp/workspace",
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: scanner,
	}
	session.ctx, session.cancel = context.WithCancel(context.Background())

	// Run loop synchronously; upon EOF it must close channels and return
	session.readStdoutLoop()
	session.cancel()

	// Wait briefly for scheduler
	time.Sleep(10 * time.Millisecond)
}

func TestGoleak_SessionKill_NoOrphanGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tmpDir := t.TempDir()
	mockScript := filepath.Join(tmpDir, "sleep_loop.sh")
	_ = os.WriteFile(mockScript, []byte("#!/bin/sh\nexec sleep 60\n"), 0755)
	t.Setenv("AGY_BINARY", mockScript)

	user := User{
		ID:        98765,
		Workspace: tmpDir,
		Model:     defaultModel,
		SessionID: "sess-leak-kill",
	}

	session := getSession("LeakBot", user, 98765)
	if session == nil {
		t.Fatalf("failed to create session")
	}

	// Start session process
	_ = session.start()

	// Give it a moment to spin up
	time.Sleep(20 * time.Millisecond)

	// Kill session
	session.Kill()

	// Wait for process and monitors to exit
	time.Sleep(50 * time.Millisecond)
}

func TestGoleak_HousekeepingWorkersShutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	t.Setenv("AGENTS_DIR", t.TempDir())
	stopWorkers := make(chan struct{})

	StartSessionGCWorker(10*time.Millisecond, 1*time.Hour, stopWorkers)
	StartDiskCleanupWorker(10*time.Millisecond, 24*time.Hour, stopWorkers)

	time.Sleep(25 * time.Millisecond)

	// Graceful shutdown
	close(stopWorkers)
	time.Sleep(25 * time.Millisecond)
}
