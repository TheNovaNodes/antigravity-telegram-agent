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

// GenerateAndSendVoice strips code blocks from text, calls ElevenLabs, and sends a Voice Note.
func GenerateAndSendVoice(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	rawEnv := os.Getenv("ELEVENLABS_API_KEY")
	if rawEnv == "" {
		return fmt.Errorf("ELEVENLABS_API_KEY not set")
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
		return fmt.Errorf("no valid sk_ keys found in ELEVENLABS_API_KEY")
	}

	apiKey := validKeys[rand.Intn(len(validKeys))]

	// 1. Strip Markdown code blocks
	reCodeBlock := regexp.MustCompile("(?s)```.*?```")
	cleanText := reCodeBlock.ReplaceAllString(text, "")

	// 2. Strip inline code
	reInlineCode := regexp.MustCompile("(?s)`.*?`")
	cleanText = reInlineCode.ReplaceAllString(cleanText, "")

	// 3. Basic cleanup
	cleanText = strings.TrimSpace(cleanText)
	if len(cleanText) == 0 {
		return nil // Empty, skip TTS
	}

	// George Voice ID (default for Russian accent)
	voiceID := "JBFqnCBsd6RMkjVDRZzb"
	url := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID)

	payload := map[string]interface{}{
		"text": cleanText,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]interface{}{
			"stability": 0.5,
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
