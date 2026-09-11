package harvester

import "time"

// ArtifactKind represents the taxonomy classification of a harvested markdown engineering document.
type ArtifactKind string

const (
	KindADR       ArtifactKind = "ADR"
	KindRFC       ArtifactKind = "RFC"
	KindResearch  ArtifactKind = "RESEARCH"
	KindChecklist ArtifactKind = "CHECKLIST"
	KindSpec      ArtifactKind = "SPEC"
	KindDoc       ArtifactKind = "DOC"
)

// ExtractedArtifact represents a normalized, classified, sanitized engineering artifact.
type ExtractedArtifact struct {
	Path            string       `json:"path"`
	OriginalPath    string       `json:"original_path"`
	Title           string       `json:"title"`
	Kind            ArtifactKind `json:"kind"`
	Content         string       `json:"content"`
	SHA256          string       `json:"sha256"`
	SessionID       string       `json:"session_id"`
	AccountSlug     string       `json:"account_slug,omitempty"`
	UserFacing      bool         `json:"user_facing"`
	RequestFeedback bool         `json:"request_feedback"`
	Summary         string       `json:"summary,omitempty"`
	ModTime         time.Time    `json:"mod_time"`
	RedactedCount   int          `json:"redacted_count"`
}

// SessionLocation tracks the filesystem paths for a discovered agent session.
type SessionLocation struct {
	SessionID          string `json:"session_id"`
	AccountSlug        string `json:"account_slug"`
	AccountHome        string `json:"account_home"`
	BrainDir           string `json:"brain_dir"`
	TranscriptPath     string `json:"transcript_path"`
	TranscriptFullPath string `json:"transcript_full_path"`
}

// ArtifactMetadata matches the sidecar JSON schema (<filename>.metadata.json).
type ArtifactMetadata struct {
	Summary         string `json:"Summary"`
	UserFacing      bool   `json:"UserFacing"`
	RequestFeedback bool   `json:"RequestFeedback"`
}

// HarvestReport provides summary statistics of an extraction cycle.
type HarvestReport struct {
	SessionID            string              `json:"session_id"`
	TotalDiscovered      int                 `json:"total_discovered"`
	TotalExtracted       int                 `json:"total_extracted"`
	RedactedSecretsCount int                 `json:"redacted_secrets_count"`
	Artifacts            []ExtractedArtifact `json:"artifacts"`
	Duration             time.Duration       `json:"duration"`
}
