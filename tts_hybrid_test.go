package main

import (
	"strings"
	"testing"
)

func TestHandleTTSEngineCommand(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(98765)

	// 1. Query current status
	SetActiveTTSEngine("")
	handleTTSEngineCommand(bot, chatID, "/tts_engine")
	ms.mu.Lock()
	numBodies := len(ms.sentBodies)
	lastBody := ""
	if numBodies > 0 {
		lastBody = ms.sentBodies[numBodies-1]
	}
	ms.mu.Unlock()

	if numBodies == 0 {
		t.Fatalf("expected message from /tts_engine query")
	}
	if !strings.Contains(lastBody, "Active+TTS+Engine") && !strings.Contains(lastBody, "Active TTS Engine") {
		t.Errorf("expected response to contain 'Active TTS Engine', got: %s", lastBody)
	}

	// 2. Switch to piper
	handleTTSEngineCommand(bot, chatID, "/tts_engine piper")
	if GetTTSEngine() != "piper" {
		t.Errorf("expected active engine to be 'piper', got: %s", GetTTSEngine())
	}

	// 3. Switch to edge
	handleTTSEngineCommand(bot, chatID, "/tts_engine edge")
	if GetTTSEngine() != "edge" {
		t.Errorf("expected active engine to be 'edge', got: %s", GetTTSEngine())
	}

	// 4. Switch to hybrid
	handleTTSEngineCommand(bot, chatID, "/tts_engine hybrid")
	if GetTTSEngine() != "hybrid" {
		t.Errorf("expected active engine to be 'hybrid', got: %s", GetTTSEngine())
	}

	// 5. Invalid engine
	handleTTSEngineCommand(bot, chatID, "/tts_engine unknown_engine")
	ms.mu.Lock()
	lastBody = ms.sentBodies[len(ms.sentBodies)-1]
	ms.mu.Unlock()

	if !strings.Contains(lastBody, "Invalid+engine") && !strings.Contains(lastBody, "Invalid engine") {
		t.Errorf("expected invalid engine warning, got: %s", lastBody)
	}
	// Engine should remain hybrid
	if GetTTSEngine() != "hybrid" {
		t.Errorf("expected engine to remain 'hybrid' after invalid switch, got: %s", GetTTSEngine())
	}

	// Clean up
	SetActiveTTSEngine("")
}

func TestHandleTTSCommand_UsageAndValidation(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(54321)

	// 1. Empty command
	handleTTSCommand(bot, chatID, "/tts")
	ms.mu.Lock()
	lastBody := ms.sentBodies[len(ms.sentBodies)-1]
	ms.mu.Unlock()
	if !strings.Contains(lastBody, "Usage") {
		t.Errorf("expected usage warning, got: %s", lastBody)
	}

	// 2. ElevenLabs engine with missing key
	SetActiveTTSEngine("elevenlabs")
	t.Setenv("ELEVENLABS_API_KEY", "")
	handleTTSCommand(bot, chatID, "/tts Test speech")
	ms.mu.Lock()
	lastBody = ms.sentBodies[len(ms.sentBodies)-1]
	ms.mu.Unlock()
	if !strings.Contains(lastBody, "ELEVENLABS_API_KEY") {
		t.Errorf("expected missing key error, got: %s", lastBody)
	}

	SetActiveTTSEngine("")
}

func TestEdgeTTS_DirectSynthesis(t *testing.T) {
	audioBytes, fn, err := GenerateVoiceEdgeTTS("Тест синтеза Edge TTS", "ru-RU-DmitryNeural")
	if err != nil {
		t.Skipf("skipping online edge-tts test due to network or rate limit: %v", err)
	}
	if len(audioBytes) == 0 {
		t.Errorf("expected non-empty audio data from edge-tts")
	}
	if fn != "voice.mp3" {
		t.Errorf("expected filename 'voice.mp3', got: %s", fn)
	}
}

func TestPiperTTS_DirectSynthesis(t *testing.T) {
	modelPath := GetPiperModel()
	if modelPath == "" {
		t.Skip("skipping Piper TTS direct test because model is not configured")
	}
	audioBytes, fn, err := GenerateVoicePiperTTS("Тест синтеза Piper TTS", modelPath)
	if err != nil {
		t.Fatalf("Piper TTS synthesis failed: %v", err)
	}
	if len(audioBytes) == 0 {
		t.Errorf("expected non-empty audio data from Piper TTS")
	}
	if fn != "voice.ogg" && fn != "voice.wav" {
		t.Errorf("expected voice.ogg or voice.wav filename, got: %s", fn)
	}
}

func TestSynthesizeVoice_HybridExecution(t *testing.T) {
	SetActiveTTSEngine("hybrid")
	defer SetActiveTTSEngine("")

	audioBytes, fn, err := SynthesizeVoice("Привет, это проверка гибридного движка.")
	if err != nil {
		t.Fatalf("SynthesizeVoice hybrid failed: %v", err)
	}
	if len(audioBytes) == 0 {
		t.Errorf("expected non-empty audio bytes from SynthesizeVoice hybrid")
	}
	if fn == "" {
		t.Errorf("expected non-empty filename")
	}
}
