package main

import (
	"testing"
	"time"
)

func TestExtractElevenLabsKeys(t *testing.T) {
	tests := []struct {
		name        string
		rawEnv      string
		expectLen   int
		expectError bool
	}{
		{
			name:        "Empty Env",
			rawEnv:      "",
			expectLen:   0,
			expectError: true,
		},
		{
			name:        "Whitespace and Delimiters Only",
			rawEnv:      "  \n \r \t , ,   ",
			expectLen:   0,
			expectError: true,
		},
		{
			name:        "Single Valid Key with sk_ prefix",
			rawEnv:      "sk_1234567890abcdef",
			expectLen:   1,
			expectError: false,
		},
		{
			name:        "Single Valid Key without prefix (legacy/custom)",
			rawEnv:      "1234567890abcdef",
			expectLen:   1,
			expectError: false,
		},
		{
			name:        "Multiple Valid Keys Comma Separated",
			rawEnv:      "sk_abc,sk_def,custom_ghi",
			expectLen:   3,
			expectError: false,
		},
		{
			name:        "Multiple Valid Keys With Spaces, Newlines and Quotes",
			rawEnv:      "sk_abc, \"sk_def\" \n 'custom_ghi'\r sk_jkl",
			expectLen:   4,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := ExtractElevenLabsKeys(tt.rawEnv)
			if (err != nil) != tt.expectError {
				t.Errorf("expected error: %v, got: %v", tt.expectError, err)
			}
			if len(keys) != tt.expectLen {
				t.Errorf("expected %d keys, got %d", tt.expectLen, len(keys))
			}
		})
	}
}

func TestCleanTextForTTS(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Normal Text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "Inline Code",
			input:    "This is `code` inline.",
			expected: "This is  inline.",
		},
		{
			name:     "Code Block",
			input:    "Check this out:\n```bash\necho 'hello'\n```\nCool right?",
			expected: "Check this out:\n\nCool right?",
		},
		{
			name:     "Multiple Code Blocks and Inline",
			input:    "Here is `some` code:\n```go\nfmt.Println(\"test\")\n```\nAnd `more` inline.",
			expected: "Here is  code:\n\nAnd  inline.",
		},
		{
			name:     "Empty After Strip",
			input:    "```bash\nonly code\n```",
			expected: "",
		},
		{
			name:     "Markdown Link",
			input:    "Check out [NovaNodes Documentation](https://novanodes.io/docs) for info.",
			expected: "Check out NovaNodes Documentation for info.",
		},
		{
			name:     "Raw URL Stripping",
			input:    "Visit https://example.com or http://test.org or file:///path/to/file directly.",
			expected: "Visit  or  or  directly.",
		},
		{
			name:     "Bold and Italic Formatting",
			input:    "This is **bold** and __underlined__ and ~~strike~~ text.",
			expected: "This is bold and underlined and strike text.",
		},
		{
			name:     "Sentence Boundary Truncation",
			input:    "First sentence. Second sentence! Third sentence? Fourth sentence.",
			expected: "First sentence. Second sentence!",
		},
		{
			name:     "Fallback Truncation Without Sentence Boundary",
			input:    "A very long sentence without any punctuation delimiter whatsoever",
			expected: "A very long sentence without any...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if tt.name == "Sentence Boundary Truncation" {
				result = CleanTextForTTS(tt.input, 35)
			} else if tt.name == "Fallback Truncation Without Sentence Boundary" {
				result = CleanTextForTTS(tt.input, 35)
			} else {
				result = CleanTextForTTS(tt.input)
			}
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestGetElevenLabsTimeoutAndMaxChars(t *testing.T) {
	// Test defaults
	t.Setenv("ELEVENLABS_TIMEOUT_SECONDS", "")
	t.Setenv("ELEVENLABS_MAX_CHARS", "")

	if to := getElevenLabsTimeout(); to != 60*time.Second {
		t.Errorf("expected default timeout 60s, got %v", to)
	}
	if mc := getMaxTTSChars(); mc != 1500 {
		t.Errorf("expected default max chars 1500, got %d", mc)
	}

	// Test overrides
	t.Setenv("ELEVENLABS_TIMEOUT_SECONDS", "45")
	t.Setenv("ELEVENLABS_MAX_CHARS", "2000")

	if to := getElevenLabsTimeout(); to != 45*time.Second {
		t.Errorf("expected overridden timeout 45s, got %v", to)
	}
	if mc := getMaxTTSChars(); mc != 2000 {
		t.Errorf("expected overridden max chars 2000, got %d", mc)
	}

	// Test invalid env fallback
	t.Setenv("ELEVENLABS_TIMEOUT_SECONDS", "invalid")
	t.Setenv("ELEVENLABS_MAX_CHARS", "-5")

	if to := getElevenLabsTimeout(); to != 60*time.Second {
		t.Errorf("expected fallback timeout 60s on invalid env, got %v", to)
	}
	if mc := getMaxTTSChars(); mc != 1500 {
		t.Errorf("expected fallback max chars 1500 on invalid env, got %d", mc)
	}
}

func TestGetTTSEngineAndOverrides(t *testing.T) {
	// Reset any runtime override
	SetActiveTTSEngine("")

	// 1. Default should be "hybrid"
	t.Setenv("TTS_ENGINE", "")
	t.Setenv("ELEVENLABS_BASE_URL", "")
	if engine := GetTTSEngine(); engine != "hybrid" {
		t.Errorf("expected default engine 'hybrid', got '%s'", engine)
	}

	// 2. Env override
	t.Setenv("TTS_ENGINE", "edge")
	if engine := GetTTSEngine(); engine != "edge" {
		t.Errorf("expected engine 'edge', got '%s'", engine)
	}

	// 3. ELEVENLABS_BASE_URL fallback when TTS_ENGINE is empty
	t.Setenv("TTS_ENGINE", "")
	t.Setenv("ELEVENLABS_BASE_URL", "http://127.0.0.1:8080")
	if engine := GetTTSEngine(); engine != "elevenlabs" {
		t.Errorf("expected engine 'elevenlabs' when ELEVENLABS_BASE_URL is set, got '%s'", engine)
	}

	// 4. Runtime override takes precedence
	SetActiveTTSEngine("piper")
	if engine := GetTTSEngine(); engine != "piper" {
		t.Errorf("expected runtime override 'piper', got '%s'", engine)
	}

	// Clean up runtime override
	SetActiveTTSEngine("")
}

func TestGetEdgeTTSVoice(t *testing.T) {
	t.Setenv("EDGE_TTS_VOICE", "")
	if v := GetEdgeTTSVoice(); v != "ru-RU-DmitryNeural" {
		t.Errorf("expected default voice 'ru-RU-DmitryNeural', got '%s'", v)
	}

	t.Setenv("EDGE_TTS_VOICE", "ru-RU-SvetlanaNeural")
	if v := GetEdgeTTSVoice(); v != "ru-RU-SvetlanaNeural" {
		t.Errorf("expected overridden voice 'ru-RU-SvetlanaNeural', got '%s'", v)
	}
}

func TestGetPiperPathAndModel(t *testing.T) {
	t.Setenv("PIPER_PATH", "/custom/bin/piper")
	if p := GetPiperPath(); p != "/custom/bin/piper" {
		t.Errorf("expected custom piper path, got '%s'", p)
	}

	t.Setenv("PIPER_MODEL", "/custom/model.onnx")
	if m := GetPiperModel(); m != "/custom/model.onnx" {
		t.Errorf("expected custom piper model, got '%s'", m)
	}
}

func TestSynthesizeVoice_EmptyText(t *testing.T) {
	audio, fn, err := SynthesizeVoice("```code only```")
	if err != nil {
		t.Errorf("expected nil error on empty text, got %v", err)
	}
	if len(audio) != 0 || fn != "" {
		t.Errorf("expected empty audio and filename on empty text, got %d bytes, fn=%s", len(audio), fn)
	}
}

func TestGenerateVoicePiperTTS_MissingModel(t *testing.T) {
	_, _, err := GenerateVoicePiperTTS("Hello world", "/nonexistent/model.onnx")
	if err == nil {
		t.Errorf("expected error for nonexistent piper model, got nil")
	}
}

