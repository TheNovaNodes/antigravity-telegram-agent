package main

import (
	"bytes"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ExtractElevenLabsKeys parses a comma- or whitespace-separated string of API keys,
// returning all non-empty tokens (supporting both sk_ and legacy/custom keys).
func ExtractElevenLabsKeys(rawEnv string) ([]string, error) {
	trimmed := strings.TrimSpace(rawEnv)
	if trimmed == "" {
		return nil, fmt.Errorf("ELEVENLABS_API_KEY not set")
	}

	keys := strings.FieldsFunc(trimmed, func(c rune) bool {
		return c == ',' || c == ' ' || c == '\n' || c == '\r'
	})

	var validKeys []string
	for _, k := range keys {
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		if k != "" {
			validKeys = append(validKeys, k)
		}
	}

	if len(validKeys) == 0 {
		return nil, fmt.Errorf("no valid keys found in ELEVENLABS_API_KEY")
	}

	return validKeys, nil
}

// CleanTextForTTS prepares a raw markdown string for Text-To-Speech generation
// by stripping out Markdown code blocks, inline code, and trimming whitespace.
func CleanTextForTTS(text string) string {
	// 1. Strip Markdown code blocks
	reCodeBlock := regexp.MustCompile("(?s)```.*?```")
	cleanText := reCodeBlock.ReplaceAllString(text, "")

	// 2. Strip inline code
	reInlineCode := regexp.MustCompile("(?s)`.*?`")
	cleanText = reInlineCode.ReplaceAllString(cleanText, "")

	// 3. Basic cleanup
	return strings.TrimSpace(cleanText)
}

// GenerateAndSendVoice acts as the Mirror Protocol's TTS engine. It sanitizes the agent's text,
// rotates between available ElevenLabs keys to bypass quotas, generates audio via the ElevenLabs API,
// and sends the resulting binary as a Telegram Voice Note.
func GenerateAndSendVoice(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	validKeys, err := ExtractElevenLabsKeys(os.Getenv("ELEVENLABS_API_KEY"))
	if err != nil {
		return err
	}

	cleanText := CleanTextForTTS(text)
	if len(cleanText) == 0 {
		return nil // Empty, skip TTS
	}

	// George Voice ID (default for Russian accent)
	voiceID := "JBFqnCBsd6RMkjVDRZzb"
	baseURL := os.Getenv("ELEVENLABS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.elevenlabs.io/v1/text-to-speech"
	}
	url := fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), voiceID)

	payload := map[string]interface{}{
		"text":     cleanText,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]interface{}{
			"stability":        0.5,
			"similarity_boost": 0.7,
		},
	}

	bodyData, _ := json.Marshal(payload)

	// Shuffle keys to distribute load evenly across key pool using crypto/rand
	shuffledKeys := make([]string, len(validKeys))
	copy(shuffledKeys, validKeys)
	for i := len(shuffledKeys) - 1; i > 0; i-- {
		n, err := crand.Int(crand.Reader, big.NewInt(int64(i+1)))
		if err == nil {
			j := int(n.Int64())
			shuffledKeys[i], shuffledKeys[j] = shuffledKeys[j], shuffledKeys[i]
		}
	}

	client := &http.Client{Timeout: 20 * time.Second}
	var lastErr error

	for _, apiKey := range shuffledKeys {
		// #nosec G704 -- gosec:nri (Need Review)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyData))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Add("xi-api-key", apiKey)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Accept", "audio/mpeg")

		// #nosec G704 -- gosec:nri (Need Review)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("ElevenLabs API error (status %d): %s", resp.StatusCode, string(body))
			continue
		}

		audioBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		// Send to Telegram as Voice Note
		fileBytes := tgbotapi.FileBytes{
			Name:  "voice.ogg",
			Bytes: audioBytes,
		}
		msg := tgbotapi.NewVoice(chatID, fileBytes)
		_, err = bot.Send(msg)
		return err
	}

	return fmt.Errorf("all ElevenLabs API keys failed, last error: %v", lastErr)
}
