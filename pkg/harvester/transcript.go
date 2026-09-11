package harvester

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TranscriptStep matches the structure of steps in transcript.jsonl / transcript_full.jsonl
type TranscriptStep struct {
	StepIndex int              `json:"step_index"`
	Source    string           `json:"source"`
	Type      string           `json:"type"`
	Status    string           `json:"status"`
	CreatedAt string           `json:"created_at"`
	ToolCalls []TranscriptCall `json:"tool_calls,omitempty"`
}

// TranscriptCall represents an invoked tool call
type TranscriptCall struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

var (
	toolWriteBytes   = []byte(`"write_to_file"`)
	toolReplaceBytes = []byte(`"replace_file_content"`)
)

// ParseTranscriptStream parses a JSONL stream of transcript steps and reconstructs all Markdown artifacts.
func ParseTranscriptStream(r io.Reader) (map[string]string, error) {
	docs := make(map[string]string)
	scanner := bufio.NewScanner(r)

	// Allocate a larger buffer for steps with large file contents (up to 4MB per step)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Fast byte filtering before costly JSON unmarshal
		if !bytes.Contains(line, toolWriteBytes) && !bytes.Contains(line, toolReplaceBytes) {
			continue
		}

		var step TranscriptStep
		if err := json.Unmarshal(line, &step); err != nil {
			// Skip malformed lines gracefully without aborting
			continue
		}

		for _, tc := range step.ToolCalls {
			switch tc.Name {
			case "write_to_file":
				target, _ := tc.Arguments["TargetFile"].(string)
				if target == "" || !strings.HasSuffix(strings.ToLower(target), ".md") {
					continue
				}
				content, _ := tc.Arguments["CodeContent"].(string)
				docs[target] = content

			case "replace_file_content":
				target, _ := tc.Arguments["TargetFile"].(string)
				if target == "" || !strings.HasSuffix(strings.ToLower(target), ".md") {
					continue
				}
				targetContent, _ := tc.Arguments["TargetContent"].(string)
				replacement, _ := tc.Arguments["ReplacementContent"].(string)

				if existing, exists := docs[target]; exists && targetContent != "" {
					docs[target] = strings.Replace(existing, targetContent, replacement, 1)
				}
			}
		}
	}

	return docs, scanner.Err()
}

// ParseTranscriptFile opens transcript_full.jsonl (or falls back to transcript.jsonl) and extracts in-flight artifacts.
func ParseTranscriptFile(sessionDir string) (map[string]string, error) {
	fullPath := filepath.Clean(filepath.Join(sessionDir, ".system_generated", "logs", "transcript_full.jsonl"))
	// #nosec G304 -- system transcript path scoped under sessionDir
	f, err := os.Open(fullPath)
	if err != nil {
		compactPath := filepath.Clean(filepath.Join(sessionDir, ".system_generated", "logs", "transcript.jsonl"))
		// #nosec G304 -- system transcript path scoped under sessionDir
		f, err = os.Open(compactPath)
		if err != nil {
			return nil, err
		}
	}
	defer f.Close()

	return ParseTranscriptStream(f)
}

// ParseAndExtractTranscriptArtifacts parses transcript files and converts resulting Markdown docs to ExtractedArtifact slice.
func ParseAndExtractTranscriptArtifacts(sessionDir string, sessionID string) ([]ExtractedArtifact, error) {
	docs, err := ParseTranscriptFile(sessionDir)
	if err != nil {
		return nil, err
	}

	var artifacts []ExtractedArtifact
	for targetPath, content := range docs {
		sanitized, redactedCount := SanitizeContent(content)
		hash := ComputeSHA256(sanitized)
		kind := ClassifyDocument(targetPath, sanitized)
		title := ExtractDocumentTitle(targetPath, sanitized)

		artifacts = append(artifacts, ExtractedArtifact{
			Path:          filepath.Base(targetPath),
			OriginalPath:  targetPath,
			Title:         title,
			Kind:          kind,
			Content:       sanitized,
			SHA256:        hash,
			SessionID:     sessionID,
			UserFacing:    true,
			ModTime:       time.Now().UTC(),
			RedactedCount: redactedCount,
		})
	}

	return artifacts, nil
}
