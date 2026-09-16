package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/wujunwei928/edge-tts-go/edge_tts"
)

var (
	activeTTSEngineMu sync.RWMutex
	activeTTSEngine   string
)

// SetActiveTTSEngine overrides the active TTS engine dynamically ("hybrid", "edge", "piper", "elevenlabs").
func SetActiveTTSEngine(engine string) {
	activeTTSEngineMu.Lock()
	defer activeTTSEngineMu.Unlock()
	activeTTSEngine = strings.ToLower(strings.TrimSpace(engine))
}

// GetTTSEngine returns the configured TTS engine ("hybrid", "edge", "piper", "elevenlabs").
// Priority: Runtime override > TTS_ENGINE env > (ELEVENLABS_BASE_URL compatibility) > default "hybrid".
func GetTTSEngine() string {
	activeTTSEngineMu.RLock()
	defer activeTTSEngineMu.RUnlock()
	if activeTTSEngine != "" {
		return activeTTSEngine
	}
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("TTS_ENGINE"))); env != "" {
		return env
	}
	// Backward compatibility: if custom ELEVENLABS_BASE_URL is explicitly set in unit tests, route to ElevenLabs
	if os.Getenv("ELEVENLABS_BASE_URL") != "" {
		return "elevenlabs"
	}
	return "hybrid"
}

// GetEdgeTTSVoice returns the configured Edge-TTS voice name (default: ru-RU-DmitryNeural).
func GetEdgeTTSVoice() string {
	if v := strings.TrimSpace(os.Getenv("EDGE_TTS_VOICE")); v != "" {
		return v
	}
	return "ru-RU-DmitryNeural"
}

// GetPiperPath returns the executable path for Piper TTS.
func GetPiperPath() string {
	if p := strings.TrimSpace(os.Getenv("PIPER_PATH")); p != "" {
		return p
	}
	if p, err := exec.LookPath("piper"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/local/bin/piper"); err == nil {
		return "/usr/local/bin/piper"
	}
	if _, err := os.Stat("/opt/piper/piper.bin"); err == nil {
		return "/opt/piper/piper.bin"
	}
	return "piper"
}

// GetPiperModel returns the ONNX model path for Piper TTS (default: ru_RU-dmitri-medium.onnx).
func GetPiperModel() string {
	if m := strings.TrimSpace(os.Getenv("PIPER_MODEL")); m != "" {
		return m
	}
	candidates := []string{
		"/opt/piper/models/ru_RU-dmitri-medium.onnx",
		"/opt/piper/ru_RU-dmitri-medium.onnx",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

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

const defaultMaxTTSChars = 1500

// getMaxTTSChars returns the maximum character length for TTS speech synthesis.
func getMaxTTSChars() int {
	if env := os.Getenv("ELEVENLABS_MAX_CHARS"); env != "" {
		if val, err := strconv.Atoi(env); err == nil && val > 0 {
			return val
		}
	}
	return defaultMaxTTSChars
}

// getElevenLabsTimeout returns the HTTP client timeout for ElevenLabs TTS generation.
func getElevenLabsTimeout() time.Duration {
	if env := os.Getenv("ELEVENLABS_TIMEOUT_SECONDS"); env != "" {
		if val, err := strconv.Atoi(env); err == nil && val > 0 {
			return time.Duration(val) * time.Second
		}
	}
	return 60 * time.Second
}

var (
	reCodeBlock  = regexp.MustCompile("(?s)```.*?```")
	reInlineCode = regexp.MustCompile("(?s)`.*?`")
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`)
	reURL        = regexp.MustCompile(`(?:https?|file)://\S+`)
)

// CleanTextForTTS prepares a raw markdown string for Text-To-Speech generation
// by stripping out Markdown code blocks, inline code, links/URLs, bold/italic markers,
// and capping length at maxChars to prevent runaway latency and quota exhaustion.
func CleanTextForTTS(text string, maxCharsOpt ...int) string {
	// 1. Strip Markdown code blocks
	cleanText := reCodeBlock.ReplaceAllString(text, "")

	// 2. Strip inline code
	cleanText = reInlineCode.ReplaceAllString(cleanText, "")

	// 3. Convert markdown links [Label](URL) to just Label
	cleanText = reLink.ReplaceAllString(cleanText, "$1")

	// 4. Strip raw URLs
	cleanText = reURL.ReplaceAllString(cleanText, "")

	// 5. Strip bold, italic, and strikethrough markers
	cleanText = strings.ReplaceAll(cleanText, "**", "")
	cleanText = strings.ReplaceAll(cleanText, "__", "")
	cleanText = strings.ReplaceAll(cleanText, "~~", "")

	cleanText = strings.TrimSpace(cleanText)

	// 6. Max length truncation at sentence boundary
	maxChars := getMaxTTSChars()
	if len(maxCharsOpt) > 0 && maxCharsOpt[0] > 0 {
		maxChars = maxCharsOpt[0]
	}

	runes := []rune(cleanText)
	if len(runes) > maxChars {
		sub := string(runes[:maxChars])
		lastSentenceEnd := strings.LastIndexAny(sub, ".!?\n")
		if lastSentenceEnd > maxChars/2 {
			cleanText = strings.TrimSpace(sub[:lastSentenceEnd+1])
		} else if lastSpace := strings.LastIndex(sub, " "); lastSpace > maxChars/2 {
			cleanText = strings.TrimSpace(sub[:lastSpace]) + "..."
		} else {
			cleanText = strings.TrimSpace(sub) + "..."
		}
	}

	return cleanText
}

// GenerateVoiceEdgeTTS generates high quality neural speech using Microsoft Edge TTS service.
func GenerateVoiceEdgeTTS(text string, voice string) ([]byte, string, error) {
	cleanText := CleanTextForTTS(text)
	if len(cleanText) == 0 {
		return nil, "", nil
	}
	if voice == "" {
		voice = GetEdgeTTSVoice()
	}

	c, err := edge_tts.NewCommunicate(cleanText,
		edge_tts.SetVoice(voice),
		edge_tts.SetOutputFormat(edge_tts.OutputFormatMP3HQ),
		edge_tts.SetReceiveTimeout(20),
	)
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts initialization failed: %w", err)
	}

	audioBytes, err := c.Stream()
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts synthesis failed: %w", err)
	}
	if len(audioBytes) == 0 {
		return nil, "", fmt.Errorf("edge-tts generated 0 bytes")
	}

	return audioBytes, "voice.mp3", nil
}

// GenerateVoicePiperTTS generates offline speech using local Piper TTS neural model on CPU.
func GenerateVoicePiperTTS(text string, modelPath string) ([]byte, string, error) {
	cleanText := CleanTextForTTS(text)
	if len(cleanText) == 0 {
		return nil, "", nil
	}
	if modelPath == "" {
		modelPath = GetPiperModel()
	}
	if modelPath == "" {
		return nil, "", fmt.Errorf("piper model not configured and default model not found")
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, "", fmt.Errorf("piper model not found at '%s': %w", modelPath, err)
	}

	piperPath := GetPiperPath()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// 1. Run Piper -> WAV output
	// #nosec G204 -- gosec:nri (Need Review)
	piperCmd := exec.CommandContext(ctx, piperPath, "-m", modelPath, "-f", "-")
	piperCmd.WaitDelay = 2 * time.Second
	piperCmd.Env = buildChildEnv("")
	piperCmd.Stdin = strings.NewReader(cleanText)
	piperCmd.Dir = filepath.Dir(piperPath)

	var wavBuf bytes.Buffer
	var piperErr bytes.Buffer
	piperCmd.Stdout = &wavBuf
	piperCmd.Stderr = &piperErr

	if err := piperCmd.Run(); err != nil {
		return nil, "", fmt.Errorf("piper execution failed: %w (stderr: %s)", err, piperErr.String())
	}
	if wavBuf.Len() == 0 {
		return nil, "", fmt.Errorf("piper produced 0 bytes of audio")
	}

	// 2. Transcode to OGG Opus via opusenc if available, otherwise return WAV
	if opusencPath, err := exec.LookPath("opusenc"); err == nil {
		// #nosec G204 -- gosec:nri (Need Review)
		opusCmd := exec.CommandContext(ctx, opusencPath, "--quiet", "-", "-")
		opusCmd.WaitDelay = 2 * time.Second
		opusCmd.Env = buildChildEnv("")
		opusCmd.Stdin = &wavBuf

		var oggBuf bytes.Buffer
		var opusErr bytes.Buffer
		opusCmd.Stdout = &oggBuf
		opusCmd.Stderr = &opusErr

		if err := opusCmd.Run(); err != nil {
			log.Printf("[TTS] opusenc transcoding failed (%v), falling back to raw WAV: %s", err, opusErr.String())
		} else if oggBuf.Len() > 0 {
			return oggBuf.Bytes(), "voice.ogg", nil
		}
	}

	return wavBuf.Bytes(), "voice.wav", nil
}

// GenerateVoiceElevenLabs generates speech via ElevenLabs API using key rotation.
func GenerateVoiceElevenLabs(text string) ([]byte, string, error) {
	validKeys, err := ExtractElevenLabsKeys(os.Getenv("ELEVENLABS_API_KEY"))
	if err != nil {
		return nil, "", err
	}

	cleanText := CleanTextForTTS(text)
	if len(cleanText) == 0 {
		return nil, "", nil
	}

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

	shuffledKeys := make([]string, len(validKeys))
	copy(shuffledKeys, validKeys)
	for i := len(shuffledKeys) - 1; i > 0; i-- {
		n, err := crand.Int(crand.Reader, big.NewInt(int64(i+1)))
		if err == nil {
			j := int(n.Int64())
			shuffledKeys[i], shuffledKeys[j] = shuffledKeys[j], shuffledKeys[i]
		}
	}

	client := &http.Client{Timeout: getElevenLabsTimeout()}
	var lastErr error
	timeoutAttempts := 0

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
			if os.IsTimeout(err) || strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "context deadline exceeded") {
				timeoutAttempts++
				if timeoutAttempts >= 2 {
					break
				}
			}
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

		return audioBytes, "voice.ogg", nil
	}

	return nil, "", fmt.Errorf("all ElevenLabs API keys failed, last error: %v", lastErr)
}

// SynthesizeVoice orchestrates Text-To-Speech using the configured engine or hybrid failover.
func SynthesizeVoice(text string) ([]byte, string, error) {
	cleanText := CleanTextForTTS(text)
	if len(cleanText) == 0 {
		return nil, "", nil
	}

	engine := GetTTSEngine()
	switch engine {
	case "edge":
		return GenerateVoiceEdgeTTS(cleanText, GetEdgeTTSVoice())

	case "piper":
		return GenerateVoicePiperTTS(cleanText, GetPiperModel())

	case "elevenlabs":
		return GenerateVoiceElevenLabs(cleanText)

	case "hybrid":
		fallthrough
	default:
		// 1. Primary: Edge-TTS (Azure Neural Free)
		audioBytes, filename, edgeErr := GenerateVoiceEdgeTTS(cleanText, GetEdgeTTSVoice())
		if edgeErr == nil && len(audioBytes) > 0 {
			return audioBytes, filename, nil
		}
		log.Printf("[TTS Hybrid] Edge-TTS failed (%v), attempting Piper TTS fallback...", edgeErr)

		// 2. Secondary Failover: Local Piper TTS (Offline CPU ONNX)
		audioBytes, filename, piperErr := GenerateVoicePiperTTS(cleanText, GetPiperModel())
		if piperErr == nil && len(audioBytes) > 0 {
			return audioBytes, filename, nil
		}
		log.Printf("[TTS Hybrid] Piper TTS failed (%v), checking ElevenLabs key pool...", piperErr)

		// 3. Tertiary Failover: ElevenLabs (if configured)
		if os.Getenv("ELEVENLABS_API_KEY") != "" {
			audioBytes, filename, elevenErr := GenerateVoiceElevenLabs(cleanText)
			if elevenErr == nil && len(audioBytes) > 0 {
				return audioBytes, filename, nil
			}
			return nil, "", fmt.Errorf("all hybrid TTS engines failed (Edge: %v; Piper: %v; ElevenLabs: %v)", edgeErr, piperErr, elevenErr)
		}

		return nil, "", fmt.Errorf("hybrid TTS failed (Edge: %v; Piper: %v)", edgeErr, piperErr)
	}
}

// GenerateAndSendVoice acts as the Mirror Protocol's TTS engine. It synthesizes voice
// via the active TTS engine (Hybrid Edge-TTS + Piper failover, or ElevenLabs) and sends
// the resulting binary as a Telegram Voice Note.
func GenerateAndSendVoice(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	audioBytes, filename, err := SynthesizeVoice(text)
	if err != nil {
		return err
	}
	if len(audioBytes) == 0 {
		return nil // Empty text after sanitization, skip
	}

	if filename == "" {
		filename = "voice.ogg"
	}

	fileBytes := tgbotapi.FileBytes{
		Name:  filename,
		Bytes: audioBytes,
	}
	msg := tgbotapi.NewVoice(chatID, fileBytes)
	_, err = bot.Send(msg)
	return err
}
