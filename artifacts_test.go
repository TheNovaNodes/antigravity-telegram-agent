package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAllowedArtifacts(t *testing.T) {
	tempDir := t.TempDir()

	mockAgentsRoot := filepath.Join(tempDir, "agents")
	mockBrainRoot := filepath.Join(tempDir, "brain")
	mockSecretRoot := filepath.Join(tempDir, "secrets")

	os.MkdirAll(mockAgentsRoot, 0755)
	os.MkdirAll(mockBrainRoot, 0755)
	os.MkdirAll(mockSecretRoot, 0755)

	t.Setenv("AGENTS_DIR", mockAgentsRoot)
	t.Setenv("BRAIN_DIR", mockBrainRoot)

	// 1. Create a valid file in mockAgentsRoot
	validAgentFile := filepath.Join(mockAgentsRoot, "test_agent_artifact.txt")
	os.WriteFile(validAgentFile, []byte("test"), 0644)

	// 2. Create a valid file in mockBrainRoot
	validBrainFile := filepath.Join(mockBrainRoot, "test_brain_artifact.txt")
	os.WriteFile(validBrainFile, []byte("test"), 0644)

	// 3. Create a malicious file outside allowed roots
	evilFile := filepath.Join(mockSecretRoot, "etc_passwd_mock.txt")
	os.WriteFile(evilFile, []byte("secret"), 0644)

	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "No links",
			text:     "Here is some text with no links.",
			expected: nil,
		},
		{
			name:     "Valid Agent File",
			text:     "Here is the file: [artifact](file://" + validAgentFile + ")",
			expected: []string{validAgentFile},
		},
		{
			name:     "Valid Brain File",
			text:     "I generated an image: [image.png](file://" + validBrainFile + ")",
			expected: []string{validBrainFile},
		},
		{
			name:     "Malicious File Blocked",
			text:     "I read your secrets: [passwd](file://" + evilFile + ")",
			expected: nil,
		},
		{
			name:     "Multiple Mixed Files",
			text:     "Agent: (file://" + validAgentFile + ")\nEvil: (file://" + evilFile + ")\nBrain: (file://" + validBrainFile + ")",
			expected: []string{validAgentFile, validBrainFile},
		},
		{
			name:     "Non-existent file",
			text:     "Does not exist: (file://" + filepath.Join(mockBrainRoot, "ghost.txt") + ")",
			expected: nil,
		},
		{
			name:     "URL Encoded path",
			text:     "Encoded: (file://" + filepath.Join(mockBrainRoot, "test%5Fbrain%5Fartifact.txt") + ")",
			expected: []string{validBrainFile},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractAllowedArtifacts(tt.text)

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d results, got %d", len(tt.expected), len(result))
			}

			for i, path := range result {
				if path != tt.expected[i] {
					t.Errorf("expected path %s, got %s", tt.expected[i], path)
				}
			}
		})
	}
}

func TestSendArtifacts_SecretRedaction(t *testing.T) {
	tempDir := t.TempDir()
	mockAgentsRoot := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgentsRoot, 0755)
	t.Setenv("AGENTS_DIR", mockAgentsRoot)

	// Create artifact with a secret token
	leakFile := filepath.Join(mockAgentsRoot, "leaky_report.md")
	rawSecret := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ_0123456"
	content := "# Report\nBot Token: " + rawSecret
	os.WriteFile(leakFile, []byte(content), 0644)

	// Call sendArtifacts with nil bot (does not send HTTP, tests file processing)
	sendArtifacts(nil, 12345, "Link: (file://"+leakFile+")")
}
