package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSession_KillConcurrencyAndIdempotency(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName:      "TestKillBot",
		ChatID:       123456,
		UserID:       789,
		Model:        "gemini-3.7-flash-high",
		Workspace:    "/root",
		Conversation: "test-conv-kill",
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
	}

	session.start()
	if !session.IsAlive() {
		t.Fatalf("expected session to be alive after start")
	}

	// Concurrently call Kill() from 30 goroutines to test idempotency and race freedom
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.Kill()
		}()
	}
	wg.Wait()

	if session.IsAlive() {
		t.Errorf("expected session to be dead after Kill()")
	}

	// Calling Kill on an already dead session must be safe and idempotent
	session.Kill()
	if session.IsAlive() {
		t.Errorf("expected session to remain dead")
	}
}

func TestSession_KillDuringActiveConcurrentReads(t *testing.T) {
	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	session := &AgySession{
		BotName:      "TestKillReadsBot",
		ChatID:       654321,
		UserID:       987,
		Model:        "gemini-3.7-flash-high",
		Workspace:    "/root",
		Conversation: "test-conv-kill-reads",
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
	}

	session.start()

	var wg sync.WaitGroup
	stopReaders := make(chan struct{})

	// Spin up goroutines reading status / session properties concurrently
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = session.IsAlive()
					_ = session.GetConversation()
					time.Sleep(1 * time.Millisecond)
				}
			}
		}()
	}

	// Allow readers to run, then trigger Kill
	time.Sleep(10 * time.Millisecond)
	session.Kill()

	close(stopReaders)
	wg.Wait()

	if session.IsAlive() {
		t.Errorf("expected session to be dead")
	}
}

func TestTTS_CustomBaseURLAndKeyRotation(t *testing.T) {
	var receivedVoiceID string
	var receivedKey string
	var receivedPayload map[string]interface{}
	var serverMutex sync.Mutex

	// Mock ElevenLabs HTTP Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverMutex.Lock()
		defer serverMutex.Unlock()

		receivedKey = r.Header.Get("xi-api-key")
		pathParts := filepath.Base(r.URL.Path)
		receivedVoiceID = pathParts

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedPayload)

		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("FAKE_MP3_AUDIO_STREAM"))
	}))
	defer ts.Close()

	os.Setenv("ELEVENLABS_BASE_URL", ts.URL)
	os.Setenv("ELEVENLABS_API_KEY", "sk_mock_test_key_12345")
	defer os.Unsetenv("ELEVENLABS_BASE_URL")
	defer os.Unsetenv("ELEVENLABS_API_KEY")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	err := GenerateAndSendVoice(bot, 1234567, "Hello from the custom base URL test!")
	if err != nil {
		t.Fatalf("GenerateAndSendVoice failed: %v", err)
	}

	serverMutex.Lock()
	defer serverMutex.Unlock()

	if receivedKey != "sk_mock_test_key_12345" {
		t.Errorf("expected xi-api-key 'sk_mock_test_key_12345', got '%s'", receivedKey)
	}
	if receivedVoiceID != "JBFqnCBsd6RMkjVDRZzb" {
		t.Errorf("expected default voice ID 'JBFqnCBsd6RMkjVDRZzb', got '%s'", receivedVoiceID)
	}
	if text, ok := receivedPayload["text"].(string); !ok || text != "Hello from the custom base URL test!" {
		t.Errorf("expected payload text 'Hello from the custom base URL test!', got '%v'", receivedPayload["text"])
	}
}

func TestGetAgyPath_EnvOverride(t *testing.T) {
	// Test custom override
	customPath := "/opt/custom/bin/agy"
	os.Setenv("AGY_BINARY", customPath)
	if path := getAgyPath(); path != customPath {
		t.Errorf("expected '%s', got '%s'", customPath, path)
	}

	// Test default fallback
	os.Unsetenv("AGY_BINARY")
	defaultPath := getAgyPath()
	if defaultPath == "" || !filepath.IsAbs(defaultPath) {
		t.Errorf("expected absolute default path, got '%s'", defaultPath)
	}
}
