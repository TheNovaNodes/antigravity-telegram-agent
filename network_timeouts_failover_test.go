package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStoreAndGetQuestionOption_UTF8Safe(t *testing.T) {
	// 1. Short option (<= 64 bytes)
	shortOpt := "Option 1: Deploy"
	cb1 := storeQuestionOption(shortOpt)
	if !strings.HasPrefix(cb1, "ans:") {
		t.Errorf("Expected ans: prefix for short option, got %s", cb1)
	}
	if len([]byte(cb1)) > 64 {
		t.Errorf("Callback length exceeds 64 bytes: %d", len([]byte(cb1)))
	}
	res1, ok1 := getQuestionOption(cb1)
	if !ok1 || res1 != shortOpt {
		t.Errorf("Expected %s, got %s (ok=%v)", shortOpt, res1, ok1)
	}

	// 2. Long UTF-8 Cyrillic option (> 64 bytes)
	longOpt := "Очень длинный ответ на русском языке с эмодзи 🚀🎭, который гарантированно превышает лимит Telegram в 64 байта!"
	cb2 := storeQuestionOption(longOpt)
	if !strings.HasPrefix(cb2, "ans_id:") {
		t.Errorf("Expected ans_id: prefix for long option, got %s", cb2)
	}
	if len([]byte(cb2)) > 64 {
		t.Errorf("Callback length exceeds 64 bytes: %d", len([]byte(cb2)))
	}
	res2, ok2 := getQuestionOption(cb2)
	if !ok2 || res2 != longOpt {
		t.Errorf("Expected full untruncated string, got %s (ok=%v)", res2, ok2)
	}
}

func TestGenerateAndSendVoice_KeyFailover(t *testing.T) {
	key1 := "sk_badkey_failover1"
	key2 := "sk_goodkey_failover2"

	attemptCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("xi-api-key")
		attemptCount++
		if apiKey == key1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"detail":"Quota exceeded"}`))
			return
		}
		if apiKey == key2 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("FAKE_AUDIO_BYTES_OK"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	os.Setenv("ELEVENLABS_API_KEY", key1+","+key2)
	os.Setenv("ELEVENLABS_BASE_URL", ts.URL)
	defer os.Unsetenv("ELEVENLABS_API_KEY")
	defer os.Unsetenv("ELEVENLABS_BASE_URL")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	err := GenerateAndSendVoice(bot, 12345, "Testing ElevenLabs multi-key failover")
	if err != nil {
		t.Fatalf("Expected failover to succeed, but got error: %v", err)
	}
}
