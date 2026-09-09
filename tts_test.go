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
