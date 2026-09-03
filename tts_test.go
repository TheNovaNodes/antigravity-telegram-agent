package main

import (
	"testing"
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
			name:        "Single Valid Key",
			rawEnv:      "sk_1234567890abcdef",
			expectLen:   1,
			expectError: false,
		},
		{
			name:        "Multiple Valid Keys Comma Separated",
			rawEnv:      "sk_abc,sk_def,sk_ghi",
			expectLen:   3,
			expectError: false,
		},
		{
			name:        "Multiple Valid Keys With Spaces and Newlines",
			rawEnv:      "sk_abc, sk_def \n sk_ghi\r sk_jkl",
			expectLen:   4,
			expectError: false,
		},
		{
			name:        "Mix of Valid and Invalid Keys",
			rawEnv:      "sk_abc, invalid_key, sk_def",
			expectLen:   2,
			expectError: false,
		},
		{
			name:        "Only Invalid Keys",
			rawEnv:      "invalid1, invalid2",
			expectLen:   0,
			expectError: true,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CleanTextForTTS(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}
