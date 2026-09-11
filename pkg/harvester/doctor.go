package harvester

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SessionInventory summarizes the harvested artifacts state for an individual session.
type SessionInventory struct {
	SessionID            string    `json:"session_id"`
	AccountSlug          string    `json:"account_slug"`
	ArtifactCount        int       `json:"artifact_count"`
	Breakdown            string    `json:"breakdown"`
	RedactedSecretsCount int       `json:"redacted_secrets_count"`
	LastModified         time.Time `json:"last_modified"`
}

// OrphanArtifact describes an engineering artifact stranded in session storage.
type OrphanArtifact struct {
	SessionID   string       `json:"session_id"`
	AccountSlug string       `json:"account_slug"`
	Path        string       `json:"path"`
	Kind        ArtifactKind `json:"kind"`
	Title       string       `json:"title"`
	AgeHours    float64      `json:"age_hours"`
	LastMod     time.Time    `json:"last_mod"`
	Size        int          `json:"size"`
	IsCommitted bool         `json:"is_committed"`
}

// discoverAllSessionLocations collects session locations across accounts and brain storage.
func discoverAllSessionLocations(accountsRoot, brainRoot string) map[string]*SessionLocation {
	if accountsRoot == "" {
		accountsRoot = defaultAccountsRoot
	}

	sessionDirs := make(map[string]*SessionLocation)

	// 1. Scan accounts directory
	if entries, err := os.ReadDir(accountsRoot); err == nil {
		for _, entry := range entries {
			accountHome := filepath.Join(accountsRoot, entry.Name())
			brainBase := filepath.Join(accountHome, ".gemini", "antigravity-cli", "brain")
			if sEntries, err := os.ReadDir(brainBase); err == nil {
				for _, se := range sEntries {
					if se.IsDir() && !strings.HasPrefix(se.Name(), ".") {
						sessionID := se.Name()
						sessionDir := filepath.Join(brainBase, sessionID)
						sessionDirs[sessionID] = buildSessionLocation(sessionID, entry.Name(), accountHome, sessionDir)
					}
				}
			}
		}
	}

	// 2. Scan fallback or provided brain root
	if brainRoot == "" {
		if env := os.Getenv("BRAIN_DIR"); env != "" {
			brainRoot = env
		} else if accountsRoot == "/etc/antigravity-bot/accounts" {
			if home, err := os.UserHomeDir(); err == nil {
				brainRoot = filepath.Join(home, ".gemini", "antigravity-cli", "brain")
			}
		}
	}

	if brainRoot != "" {
		if sEntries, err := os.ReadDir(brainRoot); err == nil {
			for _, se := range sEntries {
				if se.IsDir() && !strings.HasPrefix(se.Name(), ".") {
					sessionID := se.Name()
					if _, exists := sessionDirs[sessionID]; !exists {
						sessionDir := filepath.Join(brainRoot, sessionID)
						sessionDirs[sessionID] = buildSessionLocation(sessionID, "host", filepath.Dir(brainRoot), sessionDir)
					}
				}
			}
		}
	}

	return sessionDirs
}

// AuditAllSessions iterates through accountsRoot and brainRoot to collect artifact inventories.
func AuditAllSessions(accountsRoot, brainRoot string) ([]SessionInventory, error) {
	sessionDirs := discoverAllSessionLocations(accountsRoot, brainRoot)

	var results []SessionInventory
	for sessionID, loc := range sessionDirs {
		report, err := HarvestLocation(loc)
		if err != nil || report == nil || len(report.Artifacts) == 0 {
			continue
		}

		var latestMod time.Time
		for _, a := range report.Artifacts {
			if a.ModTime.After(latestMod) {
				latestMod = a.ModTime
			}
		}

		results = append(results, SessionInventory{
			SessionID:            sessionID,
			AccountSlug:          loc.AccountSlug,
			ArtifactCount:        len(report.Artifacts),
			Breakdown:            FormatBreakdown(report.Artifacts),
			RedactedSecretsCount: report.RedactedSecretsCount,
			LastModified:         latestMod,
		})
	}

	return results, nil
}

// ExtractSession extracts artifacts from a given session into either a directory or a ZIP file.
func ExtractSession(sessionID, outPath string) (*HarvestReport, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	if outPath == "" {
		return nil, fmt.Errorf("out path is required")
	}

	report, err := HarvestSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("harvesting session %s failed: %w", sessionID, err)
	}
	if len(report.Artifacts) == 0 {
		return report, nil
	}

	// Update Prometheus metrics
	RecordReport(report)

	cleanOut := filepath.Clean(outPath)

	if strings.HasSuffix(strings.ToLower(cleanOut), ".zip") {
		dir := filepath.Dir(cleanOut)
		base := filepath.Base(cleanOut)
		if _, err := CreateArtifactsBundleInDir(dir, base, report.Artifacts); err != nil {
			return nil, fmt.Errorf("failed to create zip bundle: %w", err)
		}
		return report, nil
	}

	// Extract into structured directory
	// #nosec G703 G301 -- outPath provided by CLI invocation
	if err := os.MkdirAll(cleanOut, 0750); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	for _, art := range report.Artifacts {
		subdir := filepath.Join(cleanOut, SubdirForKind(art.Kind))
		// #nosec G703 G301 -- subdirs scoped under cleanOut
		_ = os.MkdirAll(subdir, 0750)

		baseName := filepath.Base(art.Path)
		if !strings.HasSuffix(strings.ToLower(baseName), ".md") {
			baseName += ".md"
		}
		filePath := filepath.Join(subdir, baseName)
		// #nosec G304 G306 -- file scoped under verified subdir
		if err := os.WriteFile(filepath.Clean(filePath), []byte(art.Content), 0600); err != nil {
			return nil, fmt.Errorf("failed to write artifact %s: %w", baseName, err)
		}
	}

	manifest := BundleManifest{
		SessionID:      sessionID,
		GeneratedAt:    time.Now().UTC(),
		TotalArtifacts: len(report.Artifacts),
		Breakdown:      make(map[string]int),
		Artifacts:      make([]ManifestEntry, 0, len(report.Artifacts)),
	}
	for _, art := range report.Artifacts {
		manifest.Breakdown[string(art.Kind)]++
		manifest.Artifacts = append(manifest.Artifacts, ManifestEntry{
			ZipPath:         filepath.Join(SubdirForKind(art.Kind), filepath.Base(art.Path)),
			OriginalPath:    art.OriginalPath,
			Title:           art.Title,
			Kind:            art.Kind,
			SHA256:          art.SHA256,
			Size:            len(art.Content),
			RedactedCount:   art.RedactedCount,
			UserFacing:      art.UserFacing,
			RequestFeedback: art.RequestFeedback,
			Summary:         art.Summary,
		})
	}
	mBytes, _ := json.MarshalIndent(manifest, "", "  ")
	// #nosec G304 G306 -- manifest written to verified output directory
	_ = os.WriteFile(filepath.Join(cleanOut, "manifest.json"), mBytes, 0600)

	return report, nil
}

// RunDoctor inspects all stored artifacts older than maxAge and flags uncommitted orphans.
func RunDoctor(accountsRoot, brainRoot string, maxAge time.Duration) ([]OrphanArtifact, error) {
	if maxAge <= 0 {
		maxAge = 48 * time.Hour
	}

	sessionDirs := discoverAllSessionLocations(accountsRoot, brainRoot)

	now := time.Now().UTC()
	var orphans []OrphanArtifact

	for _, loc := range sessionDirs {
		artifacts, err := ScanBrainArtifacts(loc)
		if err != nil {
			continue
		}

		for _, art := range artifacts {
			age := now.Sub(art.ModTime)
			if age >= maxAge {
				isCommitted := checkGitCommitted(art.OriginalPath)
				if !isCommitted {
					orphans = append(orphans, OrphanArtifact{
						SessionID:   loc.SessionID,
						AccountSlug: loc.AccountSlug,
						Path:        art.Path,
						Kind:        art.Kind,
						Title:       art.Title,
						AgeHours:    age.Hours(),
						LastMod:     art.ModTime,
						Size:        len(art.Content),
						IsCommitted: false,
					})
				}
			}
		}
	}

	// Update Prometheus orphan gauge
	SetOrphanCount(len(orphans))

	return orphans, nil
}

func checkGitCommitted(filePath string) bool {
	dir := filepath.Dir(filePath)
	// Look upwards for .git directory
	gitDir := ""
	curr := dir
	for i := 0; i < 6; i++ {
		if stat, err := os.Stat(filepath.Join(curr, ".git")); err == nil && stat.IsDir() {
			gitDir = curr
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	if gitDir == "" {
		return false
	}

	// Check with git status / ls-files
	// #nosec G204 -- git command executed with fixed arguments and sanitized directory
	cmd := exec.Command("git", "-C", gitDir, "ls-files", "--error-unmatch", filePath)
	return cmd.Run() == nil
}
