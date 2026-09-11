package harvester

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditAllSessions(t *testing.T) {
	tempDir := t.TempDir()
	accountsDir := filepath.Join(tempDir, "accounts")
	accountHome := filepath.Join(accountsDir, "acc-alpha")
	brainDir := filepath.Join(accountHome, ".gemini", "antigravity-cli", "brain", "session-audit-1")
	logsDir := filepath.Join(brainDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(`{"step":1,"created_at":"2026-09-01T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Audit</USER_REQUEST>"}`), 0644)
	_ = os.WriteFile(filepath.Join(brainDir, "ADR_001_Architecture.md"), []byte("# Architecture Decision Record\nContent"), 0644)

	testBrain := filepath.Join(tempDir, "test_brain")
	_ = os.MkdirAll(testBrain, 0755)
	invList, err := AuditAllSessions(accountsDir, testBrain)
	if err != nil {
		t.Fatalf("AuditAllSessions failed: %v", err)
	}

	if len(invList) != 1 {
		t.Fatalf("Expected 1 session inventory, got %d", len(invList))
	}
	if invList[0].SessionID != "session-audit-1" {
		t.Errorf("Expected session-audit-1, got %s", invList[0].SessionID)
	}
	if invList[0].ArtifactCount != 1 {
		t.Errorf("Expected 1 artifact, got %d", invList[0].ArtifactCount)
	}
	if !strings.Contains(invList[0].Breakdown, "ADR") {
		t.Errorf("Expected ADR in breakdown, got %s", invList[0].Breakdown)
	}
}

func TestExtractSession_ToZip_And_ToDir(t *testing.T) {
	tempDir := t.TempDir()
	accountsDir := filepath.Join(tempDir, "accounts")
	accountHome := filepath.Join(accountsDir, "acc-extract")
	sessionID := "session-extract-99"
	brainDir := filepath.Join(accountHome, ".gemini", "antigravity-cli", "brain", sessionID)
	logsDir := filepath.Join(brainDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(`{"step":1,"created_at":"2026-09-01T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Extract</USER_REQUEST>"}`), 0644)
	_ = os.WriteFile(filepath.Join(brainDir, "CHECKLIST_Deploy.md"), []byte("# Checklist\n- [ ] Ready"), 0644)

	t.Setenv("ACCOUNTS_DIR", accountsDir)
	oldRoot := defaultAccountsRoot
	defaultAccountsRoot = accountsDir
	defer func() { defaultAccountsRoot = oldRoot }()

	// 1. Extract to ZIP
	zipOut := filepath.Join(tempDir, "extracted.zip")
	reportZip, err := ExtractSession(sessionID, zipOut)
	if err != nil {
		t.Fatalf("ExtractSession to ZIP failed: %v", err)
	}
	if len(reportZip.Artifacts) != 1 {
		t.Fatalf("Expected 1 artifact in zip report, got %d", len(reportZip.Artifacts))
	}

	zr, err := zip.OpenReader(zipOut)
	if err != nil {
		t.Fatalf("Failed to open extracted zip: %v", err)
	}
	zr.Close()

	// 2. Extract to Directory
	dirOut := filepath.Join(tempDir, "extracted_folder")
	reportDir, err := ExtractSession(sessionID, dirOut)
	if err != nil {
		t.Fatalf("ExtractSession to dir failed: %v", err)
	}
	if len(reportDir.Artifacts) != 1 {
		t.Fatalf("Expected 1 artifact in dir report, got %d", len(reportDir.Artifacts))
	}

	chkPath := filepath.Join(dirOut, "checklists", "CHECKLIST_Deploy.md")
	if _, err := os.Stat(chkPath); err != nil {
		t.Fatalf("Expected extracted checklist at %s: %v", chkPath, err)
	}

	manifestPath := filepath.Join(dirOut, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("Expected manifest.json at %s: %v", manifestPath, err)
	}
}

func TestRunDoctor_OrphanDetection(t *testing.T) {
	tempDir := t.TempDir()
	accountsDir := filepath.Join(tempDir, "accounts")
	accountHome := filepath.Join(accountsDir, "acc-doctor")
	sessionID := "session-doctor-old"
	brainDir := filepath.Join(accountHome, ".gemini", "antigravity-cli", "brain", sessionID)
	logsDir := filepath.Join(brainDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(`{"step":1,"created_at":"2026-09-01T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Doctor</USER_REQUEST>"}`), 0644)

	// Create an artifact with an old timestamp (72h ago)
	artPath := filepath.Join(brainDir, "SPEC_Engine.md")
	_ = os.WriteFile(artPath, []byte("# Engine Spec\nArchitecture details"), 0644)
	oldTime := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(artPath, oldTime, oldTime)

	t.Setenv("ACCOUNTS_DIR", accountsDir)
	oldRoot := defaultAccountsRoot
	defaultAccountsRoot = accountsDir
	defer func() { defaultAccountsRoot = oldRoot }()

	testBrain := filepath.Join(tempDir, "test_brain")
	_ = os.MkdirAll(testBrain, 0755)
	orphans, err := RunDoctor(accountsDir, testBrain, 48*time.Hour)
	if err != nil {
		t.Fatalf("RunDoctor failed: %v", err)
	}

	if len(orphans) != 1 {
		t.Fatalf("Expected 1 orphan detected, got %d", len(orphans))
	}
	if orphans[0].SessionID != sessionID {
		t.Errorf("Expected session %s, got %s", sessionID, orphans[0].SessionID)
	}
	if orphans[0].Kind != KindSpec {
		t.Errorf("Expected Kind SPEC, got %s", orphans[0].Kind)
	}
}
