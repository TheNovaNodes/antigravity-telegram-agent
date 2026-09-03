package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateAndSendVoice_MockServer(t *testing.T) {
	// 1. Success mock server
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ID3_MOCK_MP3_AUDIO_PAYLOAD"))
	}))
	defer successServer.Close()

	// 2. Error mock server
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":{"status":"invalid_api_key"}}`))
	}))
	defer errServer.Close()

	// Mock Telegram Bot API Server
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)

	t.Setenv("ELEVENLABS_API_KEY", "sk_mock_valid_key_1234567890abcdef")

	// Test Success
	t.Setenv("ELEVENLABS_BASE_URL", successServer.URL)
	err := GenerateAndSendVoice(bot, chatID, "Hello from successful TTS test!")
	if err != nil {
		t.Errorf("Expected nil error from mock TTS success, got %v", err)
	}

	// Test Error response
	t.Setenv("ELEVENLABS_BASE_URL", errServer.URL)
	err = GenerateAndSendVoice(bot, chatID, "Hello from failing TTS test!")
	if err == nil {
		t.Errorf("Expected error from 401 mock TTS, got nil")
	}
}
