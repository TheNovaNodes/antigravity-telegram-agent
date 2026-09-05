package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

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
	if reaped != 1 {
		t.Errorf("expected 1 process reaped, got %d", reaped)
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
