package harvester

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BundleManifest describes the contents and metadata of an exported artifacts bundle.
type BundleManifest struct {
	SessionID      string          `json:"session_id"`
	GeneratedAt    time.Time       `json:"generated_at"`
	TotalArtifacts int             `json:"total_artifacts"`
	Breakdown      map[string]int  `json:"breakdown"`
	Artifacts      []ManifestEntry `json:"artifacts"`
}

// ManifestEntry tracks metadata for an individual file archived in the bundle.
type ManifestEntry struct {
	ZipPath         string       `json:"zip_path"`
	OriginalPath    string       `json:"original_path"`
	Title           string       `json:"title"`
	Kind            ArtifactKind `json:"kind"`
	SHA256          string       `json:"sha256"`
	Size            int          `json:"size"`
	RedactedCount   int          `json:"redacted_count"`
	UserFacing      bool         `json:"user_facing"`
	RequestFeedback bool         `json:"request_feedback"`
	Summary         string       `json:"summary,omitempty"`
}

// SubdirForKind returns the clean directory name for an artifact taxonomy category.
func SubdirForKind(kind ArtifactKind) string {
	switch kind {
	case KindADR:
		return "adr"
	case KindRFC:
		return "rfc"
	case KindResearch:
		return "research"
	case KindChecklist:
		return "checklists"
	case KindSpec:
		return "specs"
	default:
		return "docs"
	}
}

// FormatBreakdown generates a human-readable taxonomy summary (e.g. "2 ADR, 1 RFC, 3 RESEARCH").
func FormatBreakdown(artifacts []ExtractedArtifact) string {
	if len(artifacts) == 0 {
		return "none"
	}
	counts := make(map[ArtifactKind]int)
	for _, art := range artifacts {
		counts[art.Kind]++
	}

	orderedKinds := []ArtifactKind{
		KindADR,
		KindRFC,
		KindResearch,
		KindChecklist,
		KindSpec,
		KindDoc,
	}

	var parts []string
	for _, k := range orderedKinds {
		if c, ok := counts[k]; ok && c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, k))
		}
	}
	if len(parts) == 0 {
		// Fallback if custom kinds
		var otherKinds []string
		for k := range counts {
			otherKinds = append(otherKinds, string(k))
		}
		sort.Strings(otherKinds)
		for _, k := range otherKinds {
			parts = append(parts, fmt.Sprintf("%d %s", counts[ArtifactKind(k)], k))
		}
	}
	return strings.Join(parts, ", ")
}

// BuildArtifactsZip streams a structured ZIP bundle of artifacts into an io.Writer.
func BuildArtifactsZip(w io.Writer, sessionID string, artifacts []ExtractedArtifact) error {
	if len(artifacts) == 0 {
		return fmt.Errorf("no artifacts to package")
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	now := time.Now().UTC()
	manifest := BundleManifest{
		SessionID:      sessionID,
		GeneratedAt:    now,
		TotalArtifacts: len(artifacts),
		Breakdown:      make(map[string]int),
		Artifacts:      make([]ManifestEntry, 0, len(artifacts)),
	}

	usedPaths := make(map[string]int)

	for _, art := range artifacts {
		manifest.Breakdown[string(art.Kind)]++

		subdir := SubdirForKind(art.Kind)
		baseName := filepath.Base(art.Path)
		if baseName == "" || baseName == "." || baseName == "/" {
			baseName = fmt.Sprintf("doc_%s.md", safePrefix(art.SHA256, 8))
		}
		if !strings.HasSuffix(strings.ToLower(baseName), ".md") {
			baseName += ".md"
		}

		targetZipPath := fmt.Sprintf("artifacts/%s/%s", subdir, baseName)
		usedPaths[targetZipPath]++
		if count := usedPaths[targetZipPath]; count > 1 {
			ext := filepath.Ext(baseName)
			nameOnly := strings.TrimSuffix(baseName, ext)
			targetZipPath = fmt.Sprintf("artifacts/%s/%s_%s%s", subdir, nameOnly, safePrefix(art.SHA256, 6), ext)
		}

		header := &zip.FileHeader{
			Name:     targetZipPath,
			Method:   zip.Deflate,
			Modified: art.ModTime,
		}
		if header.Modified.IsZero() {
			header.Modified = now
		}

		entryWriter, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("failed to create zip entry %s: %w", targetZipPath, err)
		}

		contentBytes := []byte(art.Content)
		if _, err := entryWriter.Write(contentBytes); err != nil {
			return fmt.Errorf("failed to write content to zip entry %s: %w", targetZipPath, err)
		}

		manifest.Artifacts = append(manifest.Artifacts, ManifestEntry{
			ZipPath:         targetZipPath,
			OriginalPath:    art.OriginalPath,
			Title:           art.Title,
			Kind:            art.Kind,
			SHA256:          art.SHA256,
			Size:            len(contentBytes),
			RedactedCount:   art.RedactedCount,
			UserFacing:      art.UserFacing,
			RequestFeedback: art.RequestFeedback,
			Summary:         art.Summary,
		})
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize manifest: %w", err)
	}

	manifestHeader := &zip.FileHeader{
		Name:     "artifacts/manifest.json",
		Method:   zip.Deflate,
		Modified: now,
	}
	manifestWriter, err := zw.CreateHeader(manifestHeader)
	if err != nil {
		return fmt.Errorf("failed to create manifest zip entry: %w", err)
	}
	if _, err := manifestWriter.Write(manifestBytes); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	return zw.Close()
}

// CreateArtifactsBundle generates a structured ZIP archive in a temporary file from artifacts.
func CreateArtifactsBundle(artifacts []ExtractedArtifact) (string, error) {
	if len(artifacts) == 0 {
		return "", fmt.Errorf("no artifacts to bundle")
	}

	sessionID := ""
	for _, a := range artifacts {
		if a.SessionID != "" {
			sessionID = a.SessionID
			break
		}
	}

	tmpFile, err := os.CreateTemp("", "artifacts_bundle_*.zip")
	if err != nil {
		return "", fmt.Errorf("failed to create temp zip: %w", err)
	}
	tmpPath := tmpFile.Name()

	if err := BuildArtifactsZip(tmpFile, sessionID, artifacts); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	_ = tmpFile.Close()

	return tmpPath, nil
}

// CreateArtifactsBundleInDir generates a structured ZIP archive directly into target directory.
func CreateArtifactsBundleInDir(destDir, filename string, artifacts []ExtractedArtifact) (string, error) {
	if len(artifacts) == 0 {
		return "", fmt.Errorf("no artifacts to bundle")
	}

	// #nosec G703 G301 -- directory scoped to bot scratch/exports
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create destination dir: %w", err)
	}

	sessionID := ""
	for _, a := range artifacts {
		if a.SessionID != "" {
			sessionID = a.SessionID
			break
		}
	}

	destPath := filepath.Join(destDir, filename)
	cleanPath := filepath.Clean(destPath)

	// #nosec G304 G306 -- file scoped under destDir with restricted 0600 permissions
	f, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to create zip file: %w", err)
	}

	if err := BuildArtifactsZip(f, sessionID, artifacts); err != nil {
		_ = f.Close()
		_ = os.Remove(cleanPath)
		return "", err
	}
	_ = f.Close()

	return cleanPath, nil
}

func safePrefix(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length]
}
