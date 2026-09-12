package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanMediaAndExports_RemovesExpiredFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, "agents")
	t.Setenv("AGENTS_DIR", agentsDir)

	botScratchDownloads := filepath.Join(agentsDir, "TestBot", "scratch", "downloads")
	botScratchExports := filepath.Join(agentsDir, "TestBot", "scratch", "exports")
	_ = os.MkdirAll(botScratchDownloads, 0755)
	_ = os.MkdirAll(botScratchExports, 0755)

	oldTime := time.Now().Add(-48 * time.Hour)

	// Create old expired files
	oldDownload := filepath.Join(botScratchDownloads, "old_media.ogg")
	_ = os.WriteFile(oldDownload, []byte("old audio"), 0644)
	_ = os.Chtimes(oldDownload, oldTime, oldTime)

	oldExport := filepath.Join(botScratchExports, "old_session.md")
	_ = os.WriteFile(oldExport, []byte("old session transcript"), 0644)
	_ = os.Chtimes(oldExport, oldTime, oldTime)

	// Create fresh recent files
	recentDownload := filepath.Join(botScratchDownloads, "recent_media.ogg")
	_ = os.WriteFile(recentDownload, []byte("recent audio"), 0644)

	recentExport := filepath.Join(botScratchExports, "recent_session.md")
	_ = os.WriteFile(recentExport, []byte("recent export"), 0644)

	// Clean files older than 24 hours
	cleaned := CleanMediaAndExports(24 * time.Hour)
	if cleaned != 2 {
		t.Errorf("expected 2 files cleaned, got: %d", cleaned)
	}

	if _, err := os.Stat(oldDownload); !os.IsNotExist(err) {
		t.Errorf("expected old download to be removed")
	}
	if _, err := os.Stat(oldExport); !os.IsNotExist(err) {
		t.Errorf("expected old export to be removed")
	}
	if _, err := os.Stat(recentDownload); os.IsNotExist(err) {
		t.Errorf("expected recent download to be retained")
	}
	if _, err := os.Stat(recentExport); os.IsNotExist(err) {
		t.Errorf("expected recent export to be retained")
	}
}

func TestCleanMediaAndExports_EmptyOrMissingDir(t *testing.T) {
	t.Setenv("AGENTS_DIR", filepath.Join(t.TempDir(), "nonexistent_agents"))
	cleaned := CleanMediaAndExports(24 * time.Hour)
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned for missing dir, got: %d", cleaned)
	}
}

func TestStartDiskCleanupWorker_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("AGENTS_DIR", tmpDir)

	stopChan := make(chan struct{})
	StartDiskCleanupWorker(10*time.Millisecond, 24*time.Hour, stopChan)

	// Let ticker fire at least once
	time.Sleep(30 * time.Millisecond)

	// Signal graceful shutdown
	close(stopChan)
	time.Sleep(10 * time.Millisecond)
}

func TestStartSessionGCWorker_Lifecycle(t *testing.T) {
	stopChan := make(chan struct{})
	StartSessionGCWorker(10*time.Millisecond, 1*time.Hour, stopChan)

	// Let ticker fire at least once
	time.Sleep(30 * time.Millisecond)

	// Signal graceful shutdown
	close(stopChan)
	time.Sleep(10 * time.Millisecond)
}
