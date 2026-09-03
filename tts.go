package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ExtractElevenLabsKeys parses a comma- or whitespace-separated string of API keys,
// filtering out any keys that do not start with "sk_".
func ExtractElevenLabsKeys(rawEnv string) ([]string, error) {
	if rawEnv == "" {
		return nil, fmt.Errorf("ELEVENLABS_API_KEY not set")
	}

	keys := strings.FieldsFunc(rawEnv, func(c rune) bool {
		return c == ',' || c == ' ' || c == '\n' || c == '\r'
	})

	var validKeys []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if strings.HasPrefix(k, "sk_") {
			validKeys = append(validKeys, k)
		}
	}

	if len(validKeys) == 0 {
		return nil, fmt.Errorf("no valid sk_ keys found in ELEVENLABS_API_KEY")
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

	apiKey := validKeys[rand.Intn(len(validKeys))]

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
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyData))
	if err != nil {
		return err
	}

	req.Header.Add("xi-api-key", apiKey)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "audio/mpeg")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ElevenLabs API error: %s", string(body))
	}

	audioBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
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
