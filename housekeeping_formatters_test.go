package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanIdleSessions_Eviction(t *testing.T) {
	sessionMu.Lock()
	sActive := &AgySession{
		BotName:      "ActiveBot",
		ChatID:       1001,
		UserID:       2001,
		LastActivity: time.Now(),
		isAlive:      true,
	}
	sIdle := &AgySession{
		BotName:      "IdleBot",
		ChatID:       1002,
		UserID:       2002,
		LastActivity: time.Now().Add(-48 * time.Hour),
		isAlive:      true,
	}
	globalSessions["ActiveBot:1001:2001"] = sActive
	globalSessions["IdleBot:1002:2002"] = sIdle
	sessionMu.Unlock()

	evicted := CleanIdleSessions(24 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session to be evicted, got %d", evicted)
	}

	sessionMu.Lock()
	_, activeExists := globalSessions["ActiveBot:1001:2001"]
	_, idleExists := globalSessions["IdleBot:1002:2002"]
	delete(globalSessions, "ActiveBot:1001:2001")
	sessionMu.Unlock()

	if !activeExists {
		t.Errorf("Expected active session to be preserved")
	}
	if idleExists {
		t.Errorf("Expected idle session to be deleted from globalSessions")
	}
}

func TestCleanIdleSessions_TwoHourThreshold(t *testing.T) {
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 4 * 1024 * 1024 * 1024,
			UsedRatio:      0.50, // Normal memory state (no pressure)
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sRecent := &AgySession{
		BotName:      "RecentBot",
		ChatID:       3001,
		UserID:       4001,
		LastActivity: time.Now().Add(-1 * time.Hour), // 1 hour idle: should stay (< 2h)
		isAlive:      true,
	}
	sExpired := &AgySession{
		BotName:      "ExpiredBot",
		ChatID:       3002,
		UserID:       4002,
		LastActivity: time.Now().Add(-3 * time.Hour), // 3 hours idle: should be evicted (> 2h)
		isAlive:      true,
	}
	globalSessions["RecentBot:3001:4001"] = sRecent
	globalSessions["ExpiredBot:3002:4002"] = sExpired
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session to be evicted for 2h threshold, got %d", evicted)
	}

	sessionMu.Lock()
	_, recentExists := globalSessions["RecentBot:3001:4001"]
	_, expiredExists := globalSessions["ExpiredBot:3002:4002"]
	delete(globalSessions, "RecentBot:3001:4001")
	sessionMu.Unlock()

	if !recentExists {
		t.Errorf("Expected 1-hour idle session to be preserved")
	}
	if expiredExists {
		t.Errorf("Expected 3-hour idle session to be evicted from globalSessions")
	}
}

func TestGetSystemMemoryStats(t *testing.T) {
	stats, err := getSystemMemoryStats()
	if err != nil {
		t.Skipf("Skipping on environments without /proc/meminfo: %v", err)
	}
	if stats.TotalBytes == 0 {
		t.Errorf("Expected TotalBytes > 0")
	}
	if stats.UsedRatio < 0 || stats.UsedRatio > 1.0 {
		t.Errorf("UsedRatio out of range [0, 1]: %f", stats.UsedRatio)
	}
}

func TestCleanIdleSessions_MemoryPressure_AggressiveEviction(t *testing.T) {
	// Mock high memory pressure (85% used, 500MB available)
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 500 * 1024 * 1024, // < 1.5GB
			UsedRatio:      0.85,              // >= 75%
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sRecent := &AgySession{
		BotName:      "PressureRecentBot",
		ChatID:       5001,
		UserID:       6001,
		LastActivity: time.Now().Add(-10 * time.Minute), // 10m idle: should stay (< 20m)
		isAlive:      true,
	}
	sIdleStale := &AgySession{
		BotName:      "PressureIdleBot",
		ChatID:       5002,
		UserID:       6002,
		LastActivity: time.Now().Add(-35 * time.Minute), // 35m idle: should be aggressively evicted (> 20m)
		isAlive:      true,
	}
	globalSessions["PressureRecentBot:5001:6001"] = sRecent
	globalSessions["PressureIdleBot:5002:6002"] = sIdleStale
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session aggressively evicted under memory pressure, got %d", evicted)
	}

	sessionMu.Lock()
	_, recentExists := globalSessions["PressureRecentBot:5001:6001"]
	_, idleExists := globalSessions["PressureIdleBot:5002:6002"]
	delete(globalSessions, "PressureRecentBot:5001:6001")
	sessionMu.Unlock()

	if !recentExists {
		t.Errorf("Expected 10-minute idle session to be preserved")
	}
	if idleExists {
		t.Errorf("Expected 35-minute idle session to be evicted under memory pressure")
	}
}

func TestCleanIdleSessions_ActiveTurnProtectedUnderMemoryPressure(t *testing.T) {
	// Mock extreme memory pressure (95% used, 100MB available)
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 100 * 1024 * 1024,
			UsedRatio:      0.95,
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sActiveTurn := &AgySession{
		BotName:         "ActiveTurnBot",
		ChatID:          7001,
		UserID:          8001,
		LastActivity:    time.Now().Add(-45 * time.Minute), // idle 45m, but active turn in flight!
		ActiveMessageID: 9999,
		ActiveTurnStart: time.Now().Add(-5 * time.Minute),
		isAlive:         true,
	}
	globalSessions["ActiveTurnBot:7001:8001"] = sActiveTurn
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 0 {
		t.Errorf("Expected active in-flight turn to NEVER be evicted, got %d evictions", evicted)
	}

	sessionMu.Lock()
	_, activeExists := globalSessions["ActiveTurnBot:7001:8001"]
	delete(globalSessions, "ActiveTurnBot:7001:8001")
	sessionMu.Unlock()

	if !activeExists {
		t.Errorf("Expected active in-flight session to be strictly immune to GC")
	}
}

func TestCleanOldFiles_DiskHygiene(t *testing.T) {
	tempDir := t.TempDir()
	freshFile := filepath.Join(tempDir, "fresh.txt")
	oldFile := filepath.Join(tempDir, "old.txt")

	if err := os.WriteFile(freshFile, []byte("fresh"), 0644); err != nil {
		t.Fatalf("Failed to create fresh file: %v", err)
	}
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create old file: %v", err)
	}

	// Change mtime of oldFile to 3 days ago
	oldTime := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set old mtime: %v", err)
	}

	cleaned := CleanOldFiles(tempDir, 24*time.Hour)
	if cleaned != 1 {
		t.Errorf("Expected 1 file cleaned, got %d", cleaned)
	}

	if _, err := os.Stat(freshFile); os.IsNotExist(err) {
		t.Errorf("Fresh file was unexpectedly deleted")
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("Old file was not deleted")
	}
}

func TestSplitHTMLChunks_CrossChunkTagPreservation(t *testing.T) {
	// A long code block exceeding maxChunkSize (100)
	longCode := "```go\n" + strings.Repeat("fmt.Println(\"testing long code block chunks\")\n", 10) + "```"
	html := MarkdownToTelegramHTML(longCode)

	chunks := SplitHTMLChunks(html, 150)
	if len(chunks) < 2 {
		t.Fatalf("Expected at least 2 chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		// All chunks must have balanced tags
		if strings.Count(chunk, "<pre>") != strings.Count(chunk, "</pre>") {
			t.Errorf("Chunk %d has unbalanced <pre> tags: %s", i, chunk)
		}
		if strings.Count(chunk, "<code") != strings.Count(chunk, "</code>") {
			t.Errorf("Chunk %d has unbalanced <code> tags: %s", i, chunk)
		}
	}
}
