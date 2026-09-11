package harvester

import "testing"

func TestClassifyDocument(t *testing.T) {
	tests := []struct {
		Path     string
		Content  string
		Expected ArtifactKind
	}{
		{"docs/ADR_001_storage.md", "# Storage Decision", KindADR},
		{"ADR-002-memory.md", "# Memory Management", KindADR},
		{"random.md", "# Architecture Decision Record\n## Context\n## Consequences", KindADR},
		{"RFC_001_hub.md", "# Hub Proposal", KindRFC},
		{"rfc-stream.md", "# Request for Comments\n## Motivation\n## Proposal", KindRFC},
		{"tyler_audit.md", "# Red Team Audit Report", KindResearch},
		{"market_research.md", "Findings from ecosystem research", KindResearch},
		{"gap_analysis.md", "Security gap analysis", KindResearch},
		{"CHECKLIST_release.md", "Release steps", KindChecklist},
		{"acceptance_criteria.md", "Done definition", KindChecklist},
		{"tasks.md", "- [ ] Task 1\n- [ ] Task 2\n- [x] Task 3\n- [ ] Task 4", KindChecklist},
		{"spec_v1.md", "# Specification for API", KindSpec},
		{"api_contract.md", "# API Specification", KindSpec},
		{"notes.md", "Just some random developer notes", KindDoc},
	}

	for _, tt := range tests {
		got := ClassifyDocument(tt.Path, tt.Content)
		if got != tt.Expected {
			t.Errorf("ClassifyDocument(%q) = %v; want %v", tt.Path, got, tt.Expected)
		}
	}
}

func TestExtractDocumentTitle(t *testing.T) {
	// 1. From YAML Frontmatter
	fmContent := "---\ntitle: \"Decoupled Throttler Architecture\"\nauthor: ZavLab\n---\n# Ignored Header"
	if title := ExtractDocumentTitle("arch.md", fmContent); title != "Decoupled Throttler Architecture" {
		t.Errorf("expected frontmatter title, got: %s", title)
	}

	// 2. From H1 Header
	h1Content := "\n\n# Autonomous Watchdog Specification\nSome details here..."
	if title := ExtractDocumentTitle("watchdog.md", h1Content); title != "Autonomous Watchdog Specification" {
		t.Errorf("expected H1 header title, got: %s", title)
	}

	// 3. Fallback to clean filename
	if title := ExtractDocumentTitle("session_parking_guide.md", "No headers here"); title != "Session Parking Guide" {
		t.Errorf("expected cleaned filename, got: %s", title)
	}
}
