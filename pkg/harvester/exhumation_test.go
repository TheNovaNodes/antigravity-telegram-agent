package harvester

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsServiceArtifact(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"notes.tmp", true},
		{"notes.bak", true},
		{"notes.orig", true},
		{"draft_arch.md", true},
		{"test_run.md", true},
		{"architecture_plan.md", false},
		{"SESSION_ARTIFACTS_MANIFESTO.md", false},
		{"benchmark_results.md", false},
	}

	for _, tt := range tests {
		if got := IsServiceArtifact(tt.filename); got != tt.expected {
			t.Errorf("IsServiceArtifact(%q) = %v, expected %v", tt.filename, got, tt.expected)
		}
	}
}

func TestQualifiesForExhumation(t *testing.T) {
	largeContent := strings.Repeat("a", 250)
	smallContent := "too short"

	tests := []struct {
		name     string
		art      ExtractedArtifact
		minSize  int
		expected bool
	}{
		{
			name: "Valid mature markdown artifact",
			art: ExtractedArtifact{
				Path:       "architecture.md",
				Content:    largeContent,
				UserFacing: true,
			},
			minSize:  200,
			expected: true,
		},
		{
			name: "Non-markdown file",
			art: ExtractedArtifact{
				Path:       "script.py",
				Content:    largeContent,
				UserFacing: true,
			},
			minSize:  200,
			expected: false,
		},
		{
			name: "File in scratch dir",
			art: ExtractedArtifact{
				Path:       "scratch/notes.md",
				Content:    largeContent,
				UserFacing: true,
			},
			minSize:  200,
			expected: false,
		},
		{
			name: "File below maturity threshold (<200 bytes)",
			art: ExtractedArtifact{
				Path:       "stub.md",
				Content:    smallContent,
				UserFacing: true,
			},
			minSize:  200,
			expected: false,
		},
		{
			name: "Service mask draft_*",
			art: ExtractedArtifact{
				Path:       "draft_roadmap.md",
				Content:    largeContent,
				UserFacing: true,
			},
			minSize:  200,
			expected: false,
		},
		{
			name: "UserFacing is false",
			art: ExtractedArtifact{
				Path:       "internal_doc.md",
				Content:    largeContent,
				UserFacing: false,
			},
			minSize:  200,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QualifiesForExhumation(tt.art, tt.minSize)
			if got != tt.expected {
				t.Errorf("QualifiesForExhumation() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Architecture Decision Record: Core Engine", "architecture_decision_record_core_engine"},
		{"[RFC #269] Session Exhumation Hook", "rfc_269_session_exhumation_hook"},
		{"   ", "artifact"},
		{"Benchmark_Results_v1.0", "benchmark_results_v10"},
	}

	for _, tt := range tests {
		got := SanitizeSlug(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeSlug(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestExhumeSession_FullLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	origAccountsRoot := defaultAccountsRoot
	defaultAccountsRoot = tmpDir
	defer func() { defaultAccountsRoot = origAccountsRoot }()

	sessionID := "exhumation-test-session-999"
	accHome := filepath.Join(tmpDir, "acc-exhume")
	brainDir := filepath.Join(accHome, ".gemini", "antigravity-cli", "brain", sessionID)
	scratchDir := filepath.Join(brainDir, "scratch")
	inboxDir := filepath.Join(tmpDir, "inbox")

	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		t.Fatalf("failed to create scratch dir: %v", err)
	}

	// 1. Mature ADR artifact (>200 bytes) with a secret
	adrPath := filepath.Join(brainDir, "ADR_002_core.md")
	adrContent := `# Architecture Decision Record: Pure Go Engine

## Context
We are designing a zero-downtime multi-agent platform for Telegram.
All state transitions must be cleanly audited and secret tokens like ghp_111111111122222222223333333333444444 must be redacted.

## Consequences
High stability and 100% compliance with strict git flow.`
	if err := os.WriteFile(adrPath, []byte(adrContent), 0644); err != nil {
		t.Fatalf("failed to write ADR: %v", err)
	}

	// Sidecar metadata for ADR
	adrMeta := ArtifactMetadata{
		Summary:         "Core engine ADR for session lifecycle.",
		UserFacing:      true,
		RequestFeedback: false,
	}
	metaBytes, _ := json.Marshal(adrMeta)
	if err := os.WriteFile(adrPath+".metadata.json", metaBytes, 0644); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// 2. Immature file (<200 bytes)
	stubPath := filepath.Join(brainDir, "stub.md")
	if err := os.WriteFile(stubPath, []byte("# Empty\nToo short.\n"), 0644); err != nil {
		t.Fatalf("failed to write stub: %v", err)
	}

	// 3. Scratch file
	scratchFile := filepath.Join(scratchDir, "notes.md")
	if err := os.WriteFile(scratchFile, []byte(strings.Repeat("scratch content ", 20)), 0644); err != nil {
		t.Fatalf("failed to write scratch file: %v", err)
	}

	// 4. Service mask file
	draftFile := filepath.Join(brainDir, "draft_plan.md")
	if err := os.WriteFile(draftFile, []byte(strings.Repeat("draft content ", 20)), 0644); err != nil {
		t.Fatalf("failed to write draft file: %v", err)
	}

	// Run Exhumation
	report, err := ExhumeSession(sessionID, "trickster_gobot", inboxDir, 200)
	if err != nil {
		t.Fatalf("ExhumeSession failed: %v", err)
	}

	// Verify report
	if len(report.Artifacts) != 1 {
		t.Fatalf("expected 1 exhumed artifact, got %d", len(report.Artifacts))
	}

	exhumed := report.Artifacts[0]
	if exhumed.RedactedCount != 1 {
		t.Errorf("expected 1 secret redacted in exhumed artifact, got %d", exhumed.RedactedCount)
	}

	// Verify source files in brainDir:
	// The mature ADR must be REMOVED from brainDir (Move, not Copy)
	if _, err := os.Stat(adrPath); !os.IsNotExist(err) {
		t.Errorf("expected source ADR file %s to be removed from brainDir, but it still exists", adrPath)
	}
	// Sidecar metadata must also be removed
	if _, err := os.Stat(adrPath + ".metadata.json"); !os.IsNotExist(err) {
		t.Errorf("expected sidecar metadata %s.metadata.json to be removed from brainDir, but it still exists", adrPath)
	}

	// Verify destination file in inboxDir:
	if _, err := os.Stat(exhumed.InboxPath); err != nil {
		t.Fatalf("expected exhumed file %s to exist in inboxDir: %v", exhumed.InboxPath, err)
	}

	destContent, err := os.ReadFile(exhumed.InboxPath)
	if err != nil {
		t.Fatalf("failed to read destination inbox file: %v", err)
	}
	if strings.Contains(string(destContent), "ghp_111111111122222222223333333333444444") {
		t.Errorf("secret token was not sanitized in destination file!")
	}
	if !strings.Contains(string(destContent), "[REDACTED_SECRET:GITHUB_PAT]") {
		t.Errorf("missing redacted secret marker in destination file!")
	}

	// Verify unexhumed files still exist where they were
	if _, err := os.Stat(stubPath); err != nil {
		t.Errorf("expected stub file to remain untouched: %v", err)
	}
	if _, err := os.Stat(draftFile); err != nil {
		t.Errorf("expected draft file to remain untouched: %v", err)
	}
	if _, err := os.Stat(scratchFile); err != nil {
		t.Errorf("expected scratch file to remain untouched: %v", err)
	}
}
