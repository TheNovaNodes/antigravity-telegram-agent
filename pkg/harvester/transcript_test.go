package harvester

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseTranscriptStream_Reconstruction(t *testing.T) {
	var buf bytes.Buffer

	// Step 1: user input
	s1, _ := json.Marshal(TranscriptStep{
		StepIndex: 1,
		Source:    "USER_EXPLICIT",
		Type:      "USER_INPUT",
	})
	buf.Write(s1)
	buf.WriteString("\n")

	// Step 2: write_to_file create ADR.md
	s2, _ := json.Marshal(TranscriptStep{
		StepIndex: 2,
		Source:    "MODEL",
		Type:      "PLANNER_RESPONSE",
		ToolCalls: []TranscriptCall{
			{
				Name: "write_to_file",
				Arguments: map[string]interface{}{
					"TargetFile":  "/root/ADR_001.md",
					"CodeContent": "# ADR 001\nStatus: PROPOSED\nContext: initial",
				},
			},
		},
	})
	buf.Write(s2)
	buf.WriteString("\n")

	// Step 3: replace_file_content modify ADR.md
	s3, _ := json.Marshal(TranscriptStep{
		StepIndex: 3,
		Source:    "MODEL",
		Type:      "PLANNER_RESPONSE",
		ToolCalls: []TranscriptCall{
			{
				Name: "replace_file_content",
				Arguments: map[string]interface{}{
					"TargetFile":         "/root/ADR_001.md",
					"TargetContent":      "Status: PROPOSED",
					"ReplacementContent": "Status: ACCEPTED",
				},
			},
		},
	})
	buf.Write(s3)
	buf.WriteString("\n")

	// Step 4: write_to_file a non-markdown file (should be ignored)
	s4, _ := json.Marshal(TranscriptStep{
		StepIndex: 4,
		Source:    "MODEL",
		Type:      "PLANNER_RESPONSE",
		ToolCalls: []TranscriptCall{
			{
				Name: "write_to_file",
				Arguments: map[string]interface{}{
					"TargetFile":  "/root/main.go",
					"CodeContent": "package main",
				},
			},
		},
	})
	buf.Write(s4)
	buf.WriteString("\n")

	docs, err := ParseTranscriptStream(&buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(docs) != 1 {
		t.Fatalf("expected exactly 1 reconstructed markdown doc, got %d", len(docs))
	}

	content := docs["/root/ADR_001.md"]
	if !strings.Contains(content, "Status: ACCEPTED") {
		t.Fatalf("expected modified content with 'Status: ACCEPTED', got: %s", content)
	}
}

func TestParseTranscriptStream_Benchmark1000Steps(t *testing.T) {
	var buf bytes.Buffer

	// Generate 1200 realistic steps
	for i := 1; i <= 1200; i++ {
		step := TranscriptStep{
			StepIndex: i,
			Source:    "MODEL",
			Type:      "PLANNER_RESPONSE",
		}
		if i%50 == 0 {
			step.ToolCalls = []TranscriptCall{
				{
					Name: "write_to_file",
					Arguments: map[string]interface{}{
						"TargetFile":  fmt.Sprintf("/root/doc_%d.md", i),
						"CodeContent": fmt.Sprintf("# Auto-generated Report %d\nDetails here...", i),
					},
				},
			}
		}
		data, _ := json.Marshal(step)
		buf.Write(data)
		buf.WriteString("\n")
	}

	rawBytes := buf.Bytes()

	start := time.Now()
	docs, err := ParseTranscriptStream(bytes.NewReader(rawBytes))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(docs) != 24 {
		t.Fatalf("expected 24 docs, got %d", len(docs))
	}

	// Acceptance criteria: < 100ms for 1000+ steps
	if elapsed > 100*time.Millisecond {
		t.Errorf("performance SLA violated: 1200 steps took %v (limit: 100ms)", elapsed)
	}
}
