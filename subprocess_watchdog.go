package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// procStat contains essential process state read from Linux /proc/[pid]/stat.
type procStat struct {
	PID   int
	PPID  int
	State string
}

// readProcStat parses /proc/[pid]/stat for a given process ID.
func readProcStat(pid int) (procStat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, err
	}
	content := string(data)
	lastParen := strings.LastIndex(content, ")")
	if lastParen == -1 || lastParen+2 >= len(content) {
		return procStat{}, fmt.Errorf("invalid stat format for pid %d", pid)
	}
	fields := strings.Fields(content[lastParen+2:])
	if len(fields) < 2 {
		return procStat{}, fmt.Errorf("truncated stat fields for pid %d", pid)
	}
	state := fields[0]
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procStat{}, err
	}
	return procStat{PID: pid, PPID: ppid, State: state}, nil
}

// getLinuxProcessMap returns a map of PID -> procStat for all active processes in /proc.
func getLinuxProcessMap() (map[int]procStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	procs := make(map[int]procStat, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if ps, err := readProcStat(pid); err == nil {
			procs[pid] = ps
		}
	}
	return procs, nil
}

type stoppedDescendant struct {
	PID     int
	BotName string
	State   string
}

// findStoppedDescendants identifies any process in State 'T' or 't' that is either
// a root session process or a descendant of one of rootPids.
func findStoppedDescendants(rootPids map[int]string, procs map[int]procStat) []stoppedDescendant {
	var stopped []stoppedDescendant

	for pid, proc := range procs {
		if proc.State != "T" && proc.State != "t" {
			continue
		}
		// If the root process itself is stopped
		if botName, ok := rootPids[pid]; ok {
			stopped = append(stopped, stoppedDescendant{
				PID:     pid,
				BotName: botName,
				State:   proc.State,
			})
			continue
		}
		// Trace ancestors up to root
		curr := proc.PPID
		visited := make(map[int]bool)
		for curr > 1 && !visited[curr] {
			visited[curr] = true
			if botName, ok := rootPids[curr]; ok {
				stopped = append(stopped, stoppedDescendant{
					PID:     pid,
					BotName: botName,
					State:   proc.State,
				})
				break
			}
			parentProc, ok := procs[curr]
			if !ok {
				break
			}
			curr = parentProc.PPID
		}
	}
	return stopped
}

var (
	watchdogMu       sync.Mutex
	stoppedProcSince = make(map[int]time.Time)
)

// ReapStoppedSubprocesses scans for stopped descendant processes of active agent sessions
// and terminates them if they have remained in State: T / t longer than gracePeriod.
func ReapStoppedSubprocesses(gracePeriod time.Duration) int {
	sessionMu.Lock()
	rootPids := make(map[int]string, len(globalSessions))
	for _, s := range globalSessions {
		s.mu.Lock()
		if s.Cmd != nil && s.Cmd.Process != nil && s.isAlive {
			rootPids[s.Cmd.Process.Pid] = s.BotName
		}
		s.mu.Unlock()
	}
	sessionMu.Unlock()

	if len(rootPids) == 0 {
		watchdogMu.Lock()
		clear(stoppedProcSince)
		watchdogMu.Unlock()
		return 0
	}

	procs, err := getLinuxProcessMap()
	if err != nil {
		return 0
	}

	stopped := findStoppedDescendants(rootPids, procs)

	watchdogMu.Lock()
	defer watchdogMu.Unlock()

	now := time.Now()
	activeStoppedPids := make(map[int]bool, len(stopped))
	reapedCount := 0

	for _, p := range stopped {
		activeStoppedPids[p.PID] = true
		firstSeen, seen := stoppedProcSince[p.PID]
		if !seen {
			stoppedProcSince[p.PID] = now
			firstSeen = now
		}

		if now.Sub(firstSeen) >= gracePeriod {
			// Two-phase forced termination: SIGCONT (to wake process to execute signal handlers) then SIGKILL
			_ = syscall.Kill(p.PID, syscall.SIGCONT)
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
			log.Printf("[Watchdog] ⚡ Reaped stuck stopped subprocess PID %d (state %s) under bot %s (stuck for %v)",
				p.PID, p.State, p.BotName, now.Sub(firstSeen).Round(time.Millisecond))
			delete(stoppedProcSince, p.PID)
			reapedCount++
		}
	}

	// Prune PIDs that are no longer stopped or have exited
	for pid := range stoppedProcSince {
		if !activeStoppedPids[pid] {
			delete(stoppedProcSince, pid)
		}
	}

	return reapedCount
}

// StartSubprocessWatchdogWorker runs a periodic background loop checking for stuck stopped child processes.
func StartSubprocessWatchdogWorker(interval, gracePeriod time.Duration, stopChan <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				ReapStoppedSubprocesses(gracePeriod)
			}
		}
	}()
}
