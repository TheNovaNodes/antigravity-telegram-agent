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
					"TargetFile":  "/home/user/ADR_001.md",
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
					"TargetFile":         "/home/user/ADR_001.md",
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
					"TargetFile":  "/home/user/main.go",
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

	content := docs["/home/user/ADR_001.md"]
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
						"TargetFile":  fmt.Sprintf("/home/user/doc_%d.md", i),
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

func TestExtractLastModelResponse(t *testing.T) {
	t.Run("ExtractsFinalResponseIgnoringToolCallsAndGenericSteps", func(t *testing.T) {
		lines := []string{
			`{"step_index":1,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"Please check the status"}`,
			`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","content":"","tool_calls":[{"name":"run_command"}]}`,
			`{"step_index":3,"source":"MODEL","type":"GENERIC","content":"command output: build clean"}`,
			`{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","content":"First intermediate answer"}`,
			`{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","content":null,"tool_calls":[{"name":"view_file"}]}`,
			`{"step_index":6,"source":"MODEL","type":"GENERIC","content":"file contents..."}`,
			`{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","content":"Final restored model response with 100% fidelity."}`,
		}

		raw := strings.Join(lines, "\n")
		content, err := ExtractLastModelResponse(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "Final restored model response with 100% fidelity."
		if content != expected {
			t.Errorf("expected %q, got %q", expected, content)
		}
	})

	t.Run("ResilientToCorruptedJSONLinesAndEmptyContent", func(t *testing.T) {
		lines := []string{
			`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","content":"Partial valid response"}`,
			`MALFORMED_JSON_LINE_THAT_SHOULD_BE_IGNORED{{`,
			`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","content":"   "}`,
			`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","content":"Real final response after bad line"}`,
			`ANOTHER_GARBAGE_LINE`,
		}

		raw := strings.Join(lines, "\n")
		content, err := ExtractLastModelResponse(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "Real final response after bad line"
		if content != expected {
			t.Errorf("expected %q, got %q", expected, content)
		}
	})

	t.Run("EmptyStreamOrNoModelResponseReturnsEmpty", func(t *testing.T) {
		lines := []string{
			`{"step_index":1,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"Hello"}`,
			`{"step_index":2,"source":"MODEL","type":"GENERIC","content":"some output"}`,
		}
		content, err := ExtractLastModelResponse(strings.NewReader(strings.Join(lines, "\n")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "" {
			t.Errorf("expected empty string, got %q", content)
		}

		emptyContent, err := ExtractLastModelResponse(strings.NewReader(""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if emptyContent != "" {
			t.Errorf("expected empty string for empty reader, got %q", emptyContent)
		}
	})
}
