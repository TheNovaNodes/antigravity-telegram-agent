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

func TestCleanIdleSessions_FourHourThreshold(t *testing.T) {
	sessionMu.Lock()
	sRecent := &AgySession{
		BotName:      "RecentBot",
		ChatID:       3001,
		UserID:       4001,
		LastActivity: time.Now().Add(-2 * time.Hour), // 2 hours idle: should stay
		isAlive:      true,
	}
	sExpired := &AgySession{
		BotName:      "ExpiredBot",
		ChatID:       3002,
		UserID:       4002,
		LastActivity: time.Now().Add(-5 * time.Hour), // 5 hours idle: should be evicted
		isAlive:      true,
	}
	globalSessions["RecentBot:3001:4001"] = sRecent
	globalSessions["ExpiredBot:3002:4002"] = sExpired
	sessionMu.Unlock()

	evicted := CleanIdleSessions(4 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session to be evicted for 4h threshold, got %d", evicted)
	}

	sessionMu.Lock()
	_, recentExists := globalSessions["RecentBot:3001:4001"]
	_, expiredExists := globalSessions["ExpiredBot:3002:4002"]
	delete(globalSessions, "RecentBot:3001:4001")
	sessionMu.Unlock()

	if !recentExists {
		t.Errorf("Expected 2-hour idle session to be preserved")
	}
	if expiredExists {
		t.Errorf("Expected 5-hour idle session to be evicted from globalSessions")
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
