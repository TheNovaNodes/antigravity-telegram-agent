package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// createStrictTelegramMockServer creates an HTTP server that simulates real Telegram API edge cases:
// 1. Strict length limit: rejects text > 4096 chars with 400 Bad Request: message is too long.
// 2. Strict edit check: rejects editMessageText with identical text with 400 Bad Request: message is not modified.
func createStrictTelegramMockServer(t *testing.T) (*httptest.Server, *[]string, *sync.Mutex) {
	var mu sync.Mutex
	var sentRequests []string
	lastEditedText := make(map[int]string)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		mu.Lock()
		sentRequests = append(sentRequests, string(bodyBytes))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		vals, _ := url.ParseQuery(string(bodyBytes))
		text := vals.Get("text")

		// Rule 1: Real Telegram 4096 character hard limit
		if len([]rune(text)) > 4096 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`))
			return
		}

		// Rule 2: Real Telegram message is not modified
		if strings.Contains(r.URL.Path, "editMessageText") {
			msgIDStr := vals.Get("message_id")
			var msgID int
			fmt.Sscanf(msgIDStr, "%d", &msgID)

			mu.Lock()
			prev, seen := lastEditedText[msgID]
			if seen && prev == text {
				mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}`))
				return
			}
			lastEditedText[msgID] = text
			mu.Unlock()
		}

		w.Write([]byte(`{"ok":true,"result":{"message_id":200,"chat":{"id":12345},"text":"ok"}}`))
	}))

	return ts, &sentRequests, &mu
}

func TestStrictTelegram_ChunkSplittingEnforces4096Limit(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	// Generate a massive text of 12,000 characters with various markdown blocks
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(fmt.Sprintf("### Paragraph %d\nThis is a long paragraph with **bold text**, `inline code`, and instructions.\n\n", i))
	}
	longText := sb.String()

	chunks := sendChunk(bot, 12345, 100, longText)
	if len(chunks) <= 1 {
		t.Fatalf("Expected text to be split into multiple chunks, got %d chunks", len(chunks))
	}

	// For the remaining chunks beyond chunk[0], send them as new messages like in session.go
	for i := 1; i < len(chunks); i++ {
		msg := tgbotapi.NewMessage(12345, chunks[i])
		msg.ParseMode = "HTML"
		_, err := bot.Send(msg)
		if err != nil {
			t.Fatalf("Telegram rejected chunk %d (len %d): %v", i, len(chunks[i]), err)
		}
	}

	mu.Lock()
	totalSent := len(*sentReqs)
	mu.Unlock()

	if totalSent < len(chunks) {
		t.Errorf("Expected at least %d requests sent, got %d", len(chunks), totalSent)
	}
}

func TestStrictTelegram_MessageNotModifiedSuppression(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	sameText := "Identical streaming text that is sent twice without delta change."

	// 1. First edit: succeeds (200 OK)
	sendChunk(bot, 12345, 300, sameText)

	mu.Lock()
	initialCount := len(*sentReqs)
	mu.Unlock()

	// 2. Second edit with identical content: strict mock returns 400 "message is not modified"
	// sendChunk must suppress this error and NOT trigger fallback bot.Send(newMsg)
	sendChunk(bot, 12345, 300, sameText)

	mu.Lock()
	afterCount := len(*sentReqs)
	mu.Unlock()

	// Exactly 1 new request (the editMessageText attempt) should have been made, NO fallback sendMessage
	if afterCount != initialCount+1 {
		t.Errorf("Expected 1 edit attempt without fallback, initial=%d, after=%d", initialCount, afterCount)
	}
}

func TestStrictTelegram_EditFailureTriggersNewMessageFallback(t *testing.T) {
	// Server returns 400 Bad Request: message to edit not found (generic error)
	var sentBodies []string
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		mu.Lock()
		sentBodies = append(sentBodies, string(bodyBytes))
		mu.Unlock()

		if strings.Contains(r.URL.Path, "editMessageText") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`))
			return
		}

		// Fallback sendMessage succeeds
		w.Write([]byte(`{"ok":true,"result":{"message_id":555,"chat":{"id":12345},"text":"fallback ok"}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, _ := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())

	text := "Important update when message cannot be edited."
	sendChunk(bot, 12345, 999, text)

	mu.Lock()
	count := len(sentBodies)
	mu.Unlock()

	// Should have sent 2 requests: 1 editMessageText (which failed), followed by 1 fallback sendMessage
	if count != 2 {
		t.Errorf("Expected 2 requests (edit + fallback new message), got %d: %v", count, sentBodies)
	}
}
