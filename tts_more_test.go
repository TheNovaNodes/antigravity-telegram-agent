package main

import (
	"os"
	"testing"
)

func TestGenerateAndSendVoice_NoKey(t *testing.T) {
	os.Unsetenv("ELEVENLABS_API_KEY")
	err := GenerateAndSendVoice(nil, 1234, "Hello world")
	if err == nil {
		t.Error("Expected error when ELEVENLABS_API_KEY is not set")
	}
}

func TestGenerateAndSendVoice_EmptyTextAfterClean(t *testing.T) {
	os.Setenv("ELEVENLABS_API_KEY", "sk_testkey123")
	defer os.Unsetenv("ELEVENLABS_API_KEY")

	// Text only contains code block -> CleanTextForTTS returns ""
	err := GenerateAndSendVoice(nil, 1234, "```go\nfmt.Println()\n```")
	if err != nil {
		t.Errorf("Expected nil when cleaned text is empty, got: %v", err)
	}
}
