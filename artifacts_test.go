package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAllowedArtifacts(t *testing.T) {
	// Create temporary mock directories
	tempDir := t.TempDir()
	
	// Mock the brain and agents directories inside the temp dir to avoid breaking real ones
	mockAgentsRoot := filepath.Join(tempDir, ".agents")
	mockBrainRoot := filepath.Join(tempDir, "brain")
	mockSecretRoot := filepath.Join(tempDir, "secrets")
	
	os.MkdirAll(mockAgentsRoot, 0755)
	os.MkdirAll(mockBrainRoot, 0755)
	os.MkdirAll(mockSecretRoot, 0755)

	// We can't easily mock getAgentsDir() and the hardcoded brain path in the main code
	// without injecting them. However, since the unit test runs on the actual file system,
	// we will just create real files in the REAL /root/.agents and /root/.gemini/antigravity-cli/brain 
	// (or wherever they actually point to on this machine) for testing.

	agentsDir := getAgentsDir()
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	brainDir := filepath.Join(home, ".gemini/antigravity-cli/brain")
	
	os.MkdirAll(agentsDir, 0755)
	os.MkdirAll(brainDir, 0755)

	// 1. Create a valid file in agentsDir
	validAgentFile := filepath.Join(agentsDir, "test_agent_artifact.txt")
	os.WriteFile(validAgentFile, []byte("test"), 0644)
	defer os.Remove(validAgentFile)

	// 2. Create a valid file in brainDir
	validBrainFile := filepath.Join(brainDir, "test_brain_artifact.txt")
	os.WriteFile(validBrainFile, []byte("test"), 0644)
	defer os.Remove(validBrainFile)

	// 3. Create a malicious file outside allowed roots
	evilFile := filepath.Join(os.TempDir(), "etc_passwd_mock.txt")
	os.WriteFile(evilFile, []byte("secret"), 0644)
	defer os.Remove(evilFile)

	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name: "No links",
			text: "Here is some text with no links.",
			expected: nil,
		},
		{
			name: "Valid Agent File",
			text: "Here is the file: [artifact](file://" + validAgentFile + ")",
			expected: []string{validAgentFile},
		},
		{
			name: "Valid Brain File",
			text: "I generated an image: [image.png](file://" + validBrainFile + ")",
			expected: []string{validBrainFile},
		},
		{
			name: "Malicious File Blocked",
			text: "I read your secrets: [passwd](file://" + evilFile + ")",
			expected: nil,
		},
		{
			name: "Multiple Mixed Files",
			text: "Agent: (file://" + validAgentFile + ")\nEvil: (file://" + evilFile + ")\nBrain: (file://" + validBrainFile + ")",
			expected: []string{validAgentFile, validBrainFile},
		},
		{
			name: "Non-existent file",
			text: "Does not exist: (file://" + filepath.Join(brainDir, "ghost.txt") + ")",
			expected: nil,
		},
		{
			name: "URL Encoded path",
			text: "Encoded: (file://" + filepath.Join(brainDir, "test%5Fbrain%5Fartifact.txt") + ")", // %5F is underscore
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
