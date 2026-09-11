package harvester

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSession_And_ScanBrainArtifacts(t *testing.T) {
	// Setup temporary mock accounts directory
	tmpDir := t.TempDir()
	origAccountsRoot := defaultAccountsRoot
	defaultAccountsRoot = tmpDir
	defer func() { defaultAccountsRoot = origAccountsRoot }()

	sessionID := "test-session-uuid-1234"
	accHome := filepath.Join(tmpDir, "acc-1")
	brainDir := filepath.Join(accHome, ".gemini", "antigravity-cli", "brain", sessionID)
	logsDir := filepath.Join(brainDir, ".system_generated", "logs")
	scratchDir := filepath.Join(brainDir, "scratch")

	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}
	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		t.Fatalf("failed to create scratch dir: %v", err)
	}

	// 1. Create a legitimate ADR artifact
	adrPath := filepath.Join(brainDir, "ADR_001_harvester.md")
	adrContent := "# Architecture Decision Record: Harvester\n## Context\nAutomating artifacts collection.\n## Consequences\nZero orphans."
	if err := os.WriteFile(adrPath, []byte(adrContent), 0644); err != nil {
		t.Fatalf("failed to write ADR: %v", err)
	}

	// 2. Create sidecar metadata for ADR
	adrMeta := ArtifactMetadata{
		Summary:         "Decision record for Harvester engine design.",
		UserFacing:      true,
		RequestFeedback: false,
	}
	metaBytes, _ := json.Marshal(adrMeta)
	if err := os.WriteFile(adrPath+".metadata.json", metaBytes, 0644); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// 3. Create a scratch file that should be ignored
	scratchFile := filepath.Join(scratchDir, "ignore_me.md")
	if err := os.WriteFile(scratchFile, []byte("ignored"), 0644); err != nil {
		t.Fatalf("failed to write scratch file: %v", err)
	}

	// 4. Create a system transcript log with a secret to test transcript parsing & sanitization
	transcriptPath := filepath.Join(logsDir, "transcript_full.jsonl")
	step := TranscriptStep{
		StepIndex: 1,
		Source:    "MODEL",
		Type:      "PLANNER_RESPONSE",
		ToolCalls: []TranscriptCall{
			{
				Name: "write_to_file",
				Arguments: map[string]interface{}{
					"TargetFile":  filepath.Join(brainDir, "checklist_deploy.md"),
					"CodeContent": "- [ ] Deploy step 1\n- [ ] Deploy step 2 with token ghp_111111111122222222223333333333444444\n- [ ] Step 3",
				},
			},
		},
	}
	stepBytes, _ := json.Marshal(step)
	if err := os.WriteFile(transcriptPath, append(stepBytes, '\n'), 0644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Test DiscoverSession
	loc, err := DiscoverSession(sessionID)
	if err != nil {
		t.Fatalf("DiscoverSession failed: %v", err)
	}
	if loc.AccountSlug != "acc-1" {
		t.Errorf("expected account slug 'acc-1', got %q", loc.AccountSlug)
	}
	if loc.BrainDir != brainDir {
		t.Errorf("expected brain dir %q, got %q", brainDir, loc.BrainDir)
	}

	// Test HarvestSession
	report, err := HarvestSession(sessionID)
	if err != nil {
		t.Fatalf("HarvestSession failed: %v", err)
	}

	if report.TotalExtracted != 2 {
		t.Fatalf("expected 2 extracted artifacts (1 from FS + 1 from transcript), got %d", report.TotalExtracted)
	}

	if report.RedactedSecretsCount != 1 {
		t.Errorf("expected 1 redacted secret from transcript token, got %d", report.RedactedSecretsCount)
	}

	// Verify ADR artifact properties
	var foundADR bool
	for _, art := range report.Artifacts {
		if art.Kind == KindADR {
			foundADR = true
			if art.Summary != "Decision record for Harvester engine design." {
				t.Errorf("expected sidecar summary, got: %s", art.Summary)
			}
			if !art.UserFacing {
				t.Errorf("expected UserFacing=true")
			}
		}
	}
	if !foundADR {
		t.Errorf("KindADR was not extracted")
	}
}
