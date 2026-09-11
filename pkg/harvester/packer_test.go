package harvester

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubdirForKind(t *testing.T) {
	tests := []struct {
		kind     ArtifactKind
		expected string
	}{
		{KindADR, "adr"},
		{KindRFC, "rfc"},
		{KindResearch, "research"},
		{KindChecklist, "checklists"},
		{KindSpec, "specs"},
		{KindDoc, "docs"},
		{ArtifactKind("UNKNOWN"), "docs"},
	}

	for _, tc := range tests {
		if got := SubdirForKind(tc.kind); got != tc.expected {
			t.Errorf("SubdirForKind(%v) = %q, expected %q", tc.kind, got, tc.expected)
		}
	}
}

func TestFormatBreakdown(t *testing.T) {
	// 1. Empty artifacts
	if got := FormatBreakdown(nil); got != "none" {
		t.Errorf("FormatBreakdown(nil) = %q, expected %q", got, "none")
	}

	// 2. Single kind
	arts1 := []ExtractedArtifact{
		{Kind: KindADR},
		{Kind: KindADR},
	}
	if got := FormatBreakdown(arts1); got != "2 ADR" {
		t.Errorf("FormatBreakdown = %q, expected %q", got, "2 ADR")
	}

	// 3. Multiple kinds
	arts2 := []ExtractedArtifact{
		{Kind: KindRFC},
		{Kind: KindADR},
		{Kind: KindResearch},
		{Kind: KindResearch},
		{Kind: KindChecklist},
	}
	got2 := FormatBreakdown(arts2)
	expected2 := "1 ADR, 1 RFC, 2 RESEARCH, 1 CHECKLIST"
	if got2 != expected2 {
		t.Errorf("FormatBreakdown = %q, expected %q", got2, expected2)
	}
}

func TestCreateArtifactsBundle_AndManifest(t *testing.T) {
	// Empty artifacts
	if _, err := CreateArtifactsBundle(nil); err == nil {
		t.Errorf("Expected error for empty artifacts in CreateArtifactsBundle")
	}

	artifacts := []ExtractedArtifact{
		{
			Path:          "ADR_001.md",
			OriginalPath:  "/brain/123/ADR_001.md",
			Title:         "Architecture Decision 001",
			Kind:          KindADR,
			Content:       "# ADR 001\nContext and decision.",
			SHA256:        "abc1234567890",
			SessionID:     "test-session-bundle-1",
			UserFacing:    true,
			ModTime:       time.Now().UTC(),
			RedactedCount: 1,
		},
		{
			Path:            "CHECKLIST_RELEASE.md",
			OriginalPath:    "/brain/123/CHECKLIST_RELEASE.md",
			Title:           "Release Checklist",
			Kind:            KindChecklist,
			Content:         "# Release Checklist\n- [x] All tests green",
			SHA256:          "def9876543210",
			SessionID:       "test-session-bundle-1",
			UserFacing:      true,
			RequestFeedback: true,
			Summary:         "Checklist for production deployment",
			ModTime:         time.Now().UTC(),
			RedactedCount:   0,
		},
		// Name collision test
		{
			Path:          "ADR_001.md",
			OriginalPath:  "/brain/123/sub/ADR_001.md",
			Title:         "Another ADR with same base name",
			Kind:          KindADR,
			Content:       "# ADR 001 Alt\nAnother decision.",
			SHA256:        "fedcba0987654",
			SessionID:     "test-session-bundle-1",
			UserFacing:    false,
			ModTime:       time.Now().UTC(),
			RedactedCount: 0,
		},
	}

	bundlePath, err := CreateArtifactsBundle(artifacts)
	if err != nil {
		t.Fatalf("CreateArtifactsBundle failed: %v", err)
	}
	defer os.Remove(bundlePath)

	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatalf("Failed to open generated ZIP file: %v", err)
	}
	defer zr.Close()

	files := make(map[string]*zip.File)
	for _, f := range zr.File {
		files[f.Name] = f
	}

	// Verify manifest exists
	manifestFile, ok := files["artifacts/manifest.json"]
	if !ok {
		t.Fatalf("artifacts/manifest.json not found in ZIP bundle")
	}

	rc, err := manifestFile.Open()
	if err != nil {
		t.Fatalf("Failed to read manifest from zip: %v", err)
	}
	manifestBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("Failed to read manifest bytes: %v", err)
	}

	var manifest BundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("Invalid manifest JSON: %v", err)
	}

	if manifest.SessionID != "test-session-bundle-1" {
		t.Errorf("Expected SessionID 'test-session-bundle-1', got %q", manifest.SessionID)
	}
	if manifest.TotalArtifacts != 3 {
		t.Errorf("Expected TotalArtifacts 3, got %d", manifest.TotalArtifacts)
	}
	if manifest.Breakdown[string(KindADR)] != 2 || manifest.Breakdown[string(KindChecklist)] != 1 {
		t.Errorf("Unexpected breakdown in manifest: %+v", manifest.Breakdown)
	}

	// Verify entries in ZIP
	if _, ok := files["artifacts/adr/ADR_001.md"]; !ok {
		t.Errorf("artifacts/adr/ADR_001.md missing from ZIP")
	}
	if _, ok := files["artifacts/checklists/CHECKLIST_RELEASE.md"]; !ok {
		t.Errorf("artifacts/checklists/CHECKLIST_RELEASE.md missing from ZIP")
	}

	// Verify deduplicated / disambiguated filename for collided entry
	collidedFound := false
	for name := range files {
		if strings.HasPrefix(name, "artifacts/adr/ADR_001_") && strings.HasSuffix(name, ".md") {
			collidedFound = true
			break
		}
	}
	if !collidedFound {
		t.Errorf("Collided ADR_001.md was not disambiguated with a unique name in ZIP")
	}
}

func TestCreateArtifactsBundleInDir(t *testing.T) {
	tempDir := t.TempDir()

	artifacts := []ExtractedArtifact{
		{
			Path:      "SPEC_API.md",
			Title:     "API Specification",
			Kind:      KindSpec,
			Content:   "# API Spec",
			SHA256:    "spec123",
			SessionID: "session-xyz",
		},
	}

	outPath, err := CreateArtifactsBundleInDir(tempDir, "bundle_session_xyz.zip", artifacts)
	if err != nil {
		t.Fatalf("CreateArtifactsBundleInDir failed: %v", err)
	}

	if outPath != filepath.Join(tempDir, "bundle_session_xyz.zip") {
		t.Errorf("Unexpected outPath: %s", outPath)
	}

	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("ZIP file does not exist or is empty: %v", err)
	}
}
