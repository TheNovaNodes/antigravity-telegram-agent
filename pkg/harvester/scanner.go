package harvester

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var defaultAccountsRoot = "/etc/antigravity-bot/accounts"

// DiscoverSession locates the directory for a given sessionID across all accounts or host fallback.
func DiscoverSession(sessionID string) (*SessionLocation, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID cannot be empty")
	}

	// 0. Check BRAIN_DIR env override if set
	if envBrain := os.Getenv("BRAIN_DIR"); envBrain != "" {
		sessionDir := filepath.Clean(filepath.Join(envBrain, sessionID))
		// #nosec G703 G304 -- sessionDir scoped under environment BRAIN_DIR
		if stat, err := os.Stat(sessionDir); err == nil && stat.IsDir() {
			return buildSessionLocation(sessionID, "env", envBrain, sessionDir), nil
		}
	}

	// 1. Scan /etc/antigravity-bot/accounts/*/
	entries, err := os.ReadDir(defaultAccountsRoot)
	if err == nil {
		for _, entry := range entries {
			accountHome := filepath.Join(defaultAccountsRoot, entry.Name())
			brainDir := filepath.Join(accountHome, ".gemini", "antigravity-cli", "brain", sessionID)
			if stat, err := os.Stat(brainDir); err == nil && stat.IsDir() {
				return buildSessionLocation(sessionID, entry.Name(), accountHome, brainDir), nil
			}
		}
	}

	// 2. Fallback to host home directory (~/.gemini/antigravity-cli/brain/<sessionID>)
	homeDir, err := os.UserHomeDir()
	if err == nil {
		hostBrain := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", sessionID)
		if stat, err := os.Stat(hostBrain); err == nil && stat.IsDir() {
			return buildSessionLocation(sessionID, "host", homeDir, hostBrain), nil
		}
	}

	return nil, fmt.Errorf("session %s not found in account pool or host brain", sessionID)
}

func buildSessionLocation(sessionID, accountSlug, accountHome, brainDir string) *SessionLocation {
	return &SessionLocation{
		SessionID:          sessionID,
		AccountSlug:        accountSlug,
		AccountHome:        accountHome,
		BrainDir:           brainDir,
		TranscriptPath:     filepath.Join(brainDir, ".system_generated", "logs", "transcript.jsonl"),
		TranscriptFullPath: filepath.Join(brainDir, ".system_generated", "logs", "transcript_full.jsonl"),
	}
}

// ScanBrainArtifacts discovers all markdown files written to the session brain storage.
func ScanBrainArtifacts(location *SessionLocation) ([]ExtractedArtifact, error) {
	var artifacts []ExtractedArtifact

	err := filepath.Walk(location.BrainDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			name := info.Name()
			if name == ".system_generated" || name == ".tempmediaStorage" || name == ".user_uploaded" || name == "scratch" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		cleanPath := filepath.Clean(path)
		// #nosec G304 G122 -- path verified and scoped under location.BrainDir
		contentBytes, err := os.ReadFile(cleanPath)
		if err != nil {
			return nil
		}

		content := string(contentBytes)
		sanitized, redactedCount := SanitizeContent(content)
		hash := ComputeSHA256(sanitized)
		kind := ClassifyDocument(path, sanitized)
		title := ExtractDocumentTitle(path, sanitized)

		relPath, _ := filepath.Rel(location.BrainDir, path)
		if relPath == "" {
			relPath = info.Name()
		}

		art := ExtractedArtifact{
			Path:          relPath,
			OriginalPath:  path,
			Title:         title,
			Kind:          kind,
			Content:       sanitized,
			SHA256:        hash,
			SessionID:     location.SessionID,
			AccountSlug:   location.AccountSlug,
			UserFacing:    true,
			ModTime:       info.ModTime(),
			RedactedCount: redactedCount,
		}

		// Read sidecar metadata if present (<filename>.metadata.json)
		cleanSidecar := filepath.Clean(path + ".metadata.json")
		// #nosec G304 G122 -- sidecar metadata path scoped under location.BrainDir
		if metaBytes, err := os.ReadFile(cleanSidecar); err == nil {
			var meta ArtifactMetadata
			if err := json.Unmarshal(metaBytes, &meta); err == nil {
				art.Summary = meta.Summary
				art.UserFacing = meta.UserFacing
				art.RequestFeedback = meta.RequestFeedback
			}
		}

		artifacts = append(artifacts, art)
		return nil
	})

	return artifacts, err
}

// HarvestSession runs the complete discovery, extraction, sanitization, and deduplication pipeline.
func HarvestSession(sessionID string) (*HarvestReport, error) {
	start := time.Now()

	loc, err := DiscoverSession(sessionID)
	if err != nil {
		return nil, err
	}

	report := &HarvestReport{
		SessionID: sessionID,
	}

	dedupMap := make(map[string]ExtractedArtifact)

	// 1. Filesystem scan
	fsArtifacts, _ := ScanBrainArtifacts(loc)
	for _, art := range fsArtifacts {
		dedupMap[art.SHA256] = art
		report.TotalDiscovered++
		report.RedactedSecretsCount += art.RedactedCount
	}

	// 2. Transcript replay scan
	txArtifacts, _ := ParseAndExtractTranscriptArtifacts(loc.BrainDir, sessionID)
	for _, art := range txArtifacts {
		if _, exists := dedupMap[art.SHA256]; !exists {
			art.AccountSlug = loc.AccountSlug
			dedupMap[art.SHA256] = art
			report.TotalDiscovered++
			report.RedactedSecretsCount += art.RedactedCount
		}
	}

	for _, art := range dedupMap {
		report.Artifacts = append(report.Artifacts, art)
	}

	report.TotalExtracted = len(report.Artifacts)
	report.Duration = time.Since(start)

	return report, nil
}
