package harvester

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// DefaultMinMaturityBytes defines the minimum size threshold (200 bytes) from the Manifesto.
	DefaultMinMaturityBytes = 200
	slugRegex               = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
)

// ExhumedArtifact encapsulates an artifact that has been extracted, sanitized, and moved to inbox.
type ExhumedArtifact struct {
	ExtractedArtifact
	InboxPath string `json:"inbox_path"`
}

// ExhumationReport details the results of a session exhumation run.
type ExhumationReport struct {
	SessionID            string            `json:"session_id"`
	BotName              string            `json:"bot_name"`
	InboxDir             string            `json:"inbox_dir"`
	Artifacts            []ExhumedArtifact `json:"artifacts"`
	RedactedSecretsCount int               `json:"redacted_secrets_count"`
	Duration             time.Duration     `json:"duration"`
}

// IsServiceArtifact checks if a filename matches ignore masks (*.tmp, *.bak, *.orig, draft_*, test_*).
func IsServiceArtifact(filename string) bool {
	base := strings.ToLower(filepath.Base(filename))
	if strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".bak") || strings.HasSuffix(base, ".orig") {
		return true
	}
	if strings.HasPrefix(base, "draft_") || strings.HasPrefix(base, "test_") {
		return true
	}
	return false
}

// QualifiesForExhumation applies the 4 mechanical sieves from the Session Artifacts Manifesto:
// 1. Strict .md only
// 2. Not in scratch/ or system dirs
// 3. Maturity threshold (size >= minSize)
// 4. Service masks filter (*.tmp, *.bak, *.orig, draft_*, test_*)
// 5. UserFacing flag != false
func QualifiesForExhumation(art ExtractedArtifact, minSize int) bool {
	if minSize <= 0 {
		minSize = DefaultMinMaturityBytes
	}
	// Sieve 1: Strict .md only
	if !strings.HasSuffix(strings.ToLower(art.Path), ".md") {
		return false
	}
	// Sieve 2: Not in scratch/ or system dirs
	slashPath := strings.ToLower(filepath.ToSlash(art.Path))
	if strings.HasPrefix(slashPath, "scratch/") || strings.Contains(slashPath, "/scratch/") ||
		strings.HasPrefix(slashPath, ".system_generated/") || strings.Contains(slashPath, "/.system_generated/") ||
		strings.HasPrefix(slashPath, ".user_uploaded/") || strings.Contains(slashPath, "/.user_uploaded/") {
		return false
	}
	// Sieve 3: Maturity threshold (>= 200 bytes)
	if len(strings.TrimSpace(art.Content)) < minSize {
		return false
	}
	// Sieve 4: Service masks
	if IsServiceArtifact(filepath.Base(art.Path)) {
		return false
	}
	// Sieve 5: UserFacing metadata
	if !art.UserFacing {
		return false
	}
	return true
}

// SanitizeSlug converts a title or filename into a clean slug for inbox filing.
func SanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = slugRegex.ReplaceAllString(s, "")
	s = strings.Trim(s, "_")
	if s == "" {
		return "artifact"
	}
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

// MoveFile atomically moves a file from src to dst, with fallback to copy+delete for cross-device moves.
func MoveFile(src, dst string) error {
	cleanSrc := filepath.Clean(src)
	cleanDst := filepath.Clean(dst)

	if cleanSrc == cleanDst {
		return nil
	}
	cleanDst = filepath.Clean(cleanDst)
	// #nosec G703 G301 -- destination directory scoped for exhumation
	if err := os.MkdirAll(filepath.Dir(cleanDst), 0750); err != nil {
		return fmt.Errorf("failed to create destination dir: %w", err)
	}

	// Try atomic rename first
	if err := os.Rename(cleanSrc, cleanDst); err == nil {
		return nil
	}

	// Cross-device fallback: copy and remove
	// #nosec G703 G304 -- source file scoped under session storage
	data, err := os.ReadFile(cleanSrc)
	if err != nil {
		return fmt.Errorf("failed to read source file for move: %w", err)
	}

	// #nosec G703 G304 G306 -- destination file scoped under inbox directory
	if err := os.WriteFile(cleanDst, data, 0600); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}

	_ = os.Remove(cleanSrc)
	return nil
}

// ResolveUniqueInboxPath ensures the destination file name does not overwrite existing documents.
func ResolveUniqueInboxPath(inboxDir, desiredFilename string) string {
	target := filepath.Clean(filepath.Join(inboxDir, desiredFilename))
	// #nosec G703 G304 -- target path scoped under verified inbox directory
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}

	ext := filepath.Ext(desiredFilename)
	base := strings.TrimSuffix(desiredFilename, ext)

	// Add timestamp disambiguator
	timestampTarget := filepath.Clean(filepath.Join(inboxDir, fmt.Sprintf("%s_%s%s", base, time.Now().UTC().Format("150405"), ext)))
	// #nosec G703 G304 -- timestampTarget scoped under verified inbox directory
	if _, err := os.Stat(timestampTarget); os.IsNotExist(err) {
		return timestampTarget
	}

	// Fallback incremental counter
	for i := 1; i < 1000; i++ {
		counterTarget := filepath.Clean(filepath.Join(inboxDir, fmt.Sprintf("%s_%d%s", base, i, ext)))
		// #nosec G703 G304 -- counterTarget scoped under verified inbox directory
		if _, err := os.Stat(counterTarget); os.IsNotExist(err) {
			return counterTarget
		}
	}

	return target
}

// ExhumeSession runs the complete post-mortem exhumation cycle:
// 1. Discovers session storage
// 2. Scans brain artifacts
// 3. Filters via the 4 Manifesto sieves
// 4. Sanitizes secret material
// 5. Moves (alienates) documents into inboxDir: <YYYY-MM-DD>_<bot_name>_<slug>.md
// 6. Scrubs sidecar metadata in session storage so no orphans remain
func ExhumeSession(sessionID, botName, inboxDir string, minSize int) (*ExhumationReport, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required for exhumation")
	}
	if botName == "" {
		botName = "bot"
	}
	if inboxDir == "" {
		if env := os.Getenv("ECOSYSTEM_INBOX_DIR"); env != "" {
			inboxDir = env
		} else {
			inboxDir = "/root/projects/TheNovaNodes/ecosystem-docs/inbox"
		}
	}
	inboxDir = filepath.Clean(inboxDir)
	if minSize <= 0 {
		minSize = DefaultMinMaturityBytes
	}

	start := time.Now()
	report := &ExhumationReport{
		SessionID: sessionID,
		BotName:   botName,
		InboxDir:  inboxDir,
	}

	loc, err := DiscoverSession(sessionID)
	if err != nil {
		return nil, err
	}

	rawArtifacts, err := ScanBrainArtifacts(loc)
	if err != nil {
		return nil, fmt.Errorf("failed to scan artifacts in %s: %w", loc.BrainDir, err)
	}

	// Ensure destination inbox directory exists
	// #nosec G703 G301 -- inboxDir configured or defaulted to ecosystem inbox
	_ = os.MkdirAll(inboxDir, 0750)

	today := time.Now().UTC().Format("2006-01-02")

	for _, art := range rawArtifacts {
		if !QualifiesForExhumation(art, minSize) {
			continue
		}

		slug := SanitizeSlug(art.Title)
		if slug == "artifact" || len(slug) < 3 {
			slug = SanitizeSlug(strings.TrimSuffix(filepath.Base(art.Path), ".md"))
		}

		filename := fmt.Sprintf("%s_%s_%s.md", today, botName, slug)
		destPath := ResolveUniqueInboxPath(inboxDir, filename)

		// Alienate to inbox: write sanitized content to destination file
		// #nosec G703 G304 G306 -- destPath scoped under verified inbox directory
		if writeErr := os.WriteFile(destPath, []byte(art.Content), 0600); writeErr == nil {
			// Remove source file from session directory (Move, not Copy)
			if art.OriginalPath != "" {
				_ = os.Remove(art.OriginalPath)
				// Remove sidecar metadata if present (<filename>.metadata.json)
				_ = os.Remove(art.OriginalPath + ".metadata.json")
			}
		} else {
			// Fallback: try MoveFile
			if art.OriginalPath != "" {
				_ = MoveFile(art.OriginalPath, destPath)
				_ = os.Remove(art.OriginalPath + ".metadata.json")
			}
		}

		report.Artifacts = append(report.Artifacts, ExhumedArtifact{
			ExtractedArtifact: art,
			InboxPath:         destPath,
		})
		report.RedactedSecretsCount += art.RedactedCount
	}

	report.Duration = time.Since(start)
	return report, nil
}
