package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// GenerateAndSendVoice strips code blocks from text, calls ElevenLabs, and sends a Voice Note.
func GenerateAndSendVoice(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ELEVENLABS_API_KEY not set")
	}

	// 1. Strip Markdown code blocks
	reCodeBlock := regexp.MustCompile("(?s)```.*?```")
	cleanText := reCodeBlock.ReplaceAllString(text, "")

	// 2. Strip inline code
	reInlineCode := regexp.MustCompile("(?s)`.*?`")
	cleanText = reInlineCode.ReplaceAllString(cleanText, "")

	// 3. Basic cleanup
	cleanText = strings.TrimSpace(cleanText)
	if len(cleanText) == 0 || len(cleanText) > 1000 {
		return nil // Too long or empty, skip TTS
	}

	// Rachel Voice ID (default English/Multilingual)
	voiceID := "21m00Tcm4TlvDq8ikWAM"
	url := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID)

	payload := map[string]interface{}{
		"text":     cleanText,
		"model_id": "eleven_multilingual_v2",
	}
	jsonPayload, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
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
		Name:  "voice.mp3",
		Bytes: audioBytes,
	}
	msg := tgbotapi.NewVoice(chatID, fileBytes)
	_, err = bot.Send(msg)
	return err
}
