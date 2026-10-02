package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestInputRichMessage_Serialization(t *testing.T) {
	// 1. Serialization with Markdown only
	msg := InputRichMessage{
		Markdown: "# Executive Summary\n| Metric | Value |\n|---|---|\n| Latency | 5ms |",
	}
	bytesData, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var unmarshaled InputRichMessage
	if err := json.Unmarshal(bytesData, &unmarshaled); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if unmarshaled.Markdown != msg.Markdown {
		t.Errorf("expected markdown %q, got %q", msg.Markdown, unmarshaled.Markdown)
	}
	if unmarshaled.Thinking != nil {
		t.Errorf("expected thinking to be nil, got %+v", unmarshaled.Thinking)
	}

	// 2. Serialization with Thinking block
	msgWithThinking := InputRichMessage{
		Markdown: "## Final Answer\nSystem normalized.",
		Thinking: &RichBlockThinking{
			Text:      "Checking logs and cluster nodes...",
			Collapsed: true,
		},
	}
	bytesWithThinking, err := json.Marshal(msgWithThinking)
	if err != nil {
		t.Fatalf("json.Marshal with thinking failed: %v", err)
	}

	var unmarshaledThinking InputRichMessage
	if err := json.Unmarshal(bytesWithThinking, &unmarshaledThinking); err != nil {
		t.Fatalf("json.Unmarshal with thinking failed: %v", err)
	}
	if unmarshaledThinking.Thinking == nil {
		t.Fatalf("expected thinking block, got nil")
	}
	if unmarshaledThinking.Thinking.Text != "Checking logs and cluster nodes..." {
		t.Errorf("unexpected thinking text: %s", unmarshaledThinking.Thinking.Text)
	}
	if !unmarshaledThinking.Thinking.Collapsed {
		t.Errorf("expected collapsed to be true")
	}
}

func TestHasMarkdownTable(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "Standard markdown table",
			text:     "Here is the report:\n| Service | Status |\n|:---|:---|\n| API | OK |\n| DB | OK |",
			expected: true,
		},
		{
			name:     "Centered and right-aligned table",
			text:     "| ID | Name | Score |\n| :--- | :---: | ---: |\n| 1 | Alice | 99 |",
			expected: true,
		},
		{
			name:     "Simple dashed table separator",
			text:     "| A | B |\n|---|---|\n| 1 | 2 |",
			expected: true,
		},
		{
			name:     "Plain text without table",
			text:     "Hello, this is regular markdown with no tables.",
			expected: false,
		},
		{
			name:     "Pipes in text without separator",
			text:     "Option A | Option B | Option C\nNo table here.",
			expected: false,
		},
		{
			name:     "Fenced code block without table separator",
			text:     "```\n| inside | code | block |\n```",
			expected: false,
		},
		{
			name:     "Empty text",
			text:     "",
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasMarkdownTable(tc.text)
			if got != tc.expected {
				t.Errorf("HasMarkdownTable() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestExtractThinkingAndMarkdown(t *testing.T) {
	// Case 1: Standard <thought> tag
	text1 := "<thought>\nAnalyzing database query latency.\nOptimizing indexes.\n</thought>\n# Solution\nIndex added."
	cleanMd1, thinking1 := ExtractThinkingAndMarkdown(text1)
	if thinking1 == nil {
		t.Fatalf("expected thinking1 to be extracted")
	}
	if thinking1.Text != "Analyzing database query latency.\nOptimizing indexes." {
		t.Errorf("unexpected thinking text: %q", thinking1.Text)
	}
	if cleanMd1 != "# Solution\nIndex added." {
		t.Errorf("unexpected clean markdown: %q", cleanMd1)
	}

	// Case 2: DeepSeek <think> tag
	text2 := "<think>DeepSeek thought process</think>Direct answer."
	cleanMd2, thinking2 := ExtractThinkingAndMarkdown(text2)
	if thinking2 == nil || thinking2.Text != "DeepSeek thought process" {
		t.Fatalf("failed to extract <think>: %+v", thinking2)
	}
	if cleanMd2 != "Direct answer." {
		t.Errorf("unexpected markdown: %q", cleanMd2)
	}

	// Case 3: Claude <thinking> tag
	text3 := "<thinking>Claude reasoning</thinking>Output."
	cleanMd3, thinking3 := ExtractThinkingAndMarkdown(text3)
	if thinking3 == nil || thinking3.Text != "Claude reasoning" {
		t.Fatalf("failed to extract <thinking>: %+v", thinking3)
	}
	if cleanMd3 != "Output." {
		t.Errorf("unexpected markdown: %q", cleanMd3)
	}

	// Case 4: No thinking tags
	text4 := "Simple answer without thoughts."
	cleanMd4, thinking4 := ExtractThinkingAndMarkdown(text4)
	if thinking4 != nil {
		t.Errorf("expected nil thinking, got %+v", thinking4)
	}
	if cleanMd4 != text4 {
		t.Errorf("expected clean markdown %q, got %q", text4, cleanMd4)
	}

	// Case 5: ONLY thought tags (edge case: markdown should not be empty)
	text5 := "<thought>Only internal monologue</thought>"
	cleanMd5, thinking5 := ExtractThinkingAndMarkdown(text5)
	if cleanMd5 != "Only internal monologue" {
		t.Errorf("expected clean markdown to retain thought when main is empty, got %q", cleanMd5)
	}
	if thinking5 != nil {
		t.Errorf("expected promoted thinking to be nil, got %+v", thinking5)
	}
}

func TestSanitizeRichMessageText(t *testing.T) {
	// Under limit
	underLimit := strings.Repeat("A", 1000)
	if SanitizeRichMessageText(underLimit) != underLimit {
		t.Errorf("under limit text modified unexpectedly")
	}

	// Exact limit
	exactLimit := strings.Repeat("B", MaxRichMessageLength)
	if SanitizeRichMessageText(exactLimit) != exactLimit {
		t.Errorf("exact limit text modified unexpectedly")
	}

	// Over limit
	overLimit := strings.Repeat("C", 40000)
	sanitized := SanitizeRichMessageText(overLimit)
	if utf8.RuneCountInString(sanitized) != MaxRichMessageLength {
		t.Errorf("expected length %d, got %d", MaxRichMessageLength, utf8.RuneCountInString(sanitized))
	}

	// Multibyte characters (Cyrillic + Emojis)
	multibyteSeed := "Привет мир 🚀⚡ "
	var sb strings.Builder
	for sb.Len() < 100000 {
		sb.WriteString(multibyteSeed)
	}
	multibyteSanitized := SanitizeRichMessageText(sb.String())
	runeCount := utf8.RuneCountInString(multibyteSanitized)
	if runeCount != MaxRichMessageLength {
		t.Errorf("expected rune count %d for multibyte text, got %d", MaxRichMessageLength, runeCount)
	}
	if !utf8.ValidString(multibyteSanitized) {
		t.Errorf("sanitized text contains invalid UTF-8 sequences")
	}
}

func TestShouldUseRichMessage(t *testing.T) {
	// Short text without table
	if ShouldUseRichMessage("Short message") {
		t.Errorf("short message without table should not use rich message")
	}

	// Short text with table
	tableText := "Summary:\n| Col1 | Col2 |\n|---|---|\n| A | B |"
	if !ShouldUseRichMessage(tableText) {
		t.Errorf("short message with table SHOULD use rich message")
	}

	// Long text (> 4000 runes) without table
	longText := strings.Repeat("A", 4001)
	if !ShouldUseRichMessage(longText) {
		t.Errorf("text > 4000 runes SHOULD use rich message")
	}

	// Exactly 4000 runes without table
	borderText := strings.Repeat("A", 4000)
	if ShouldUseRichMessage(borderText) {
		t.Errorf("text == 4000 runes without table should use classic message for 100%% compatibility")
	}

	// Text between 4001 and 32768 runes
	validRichText := strings.Repeat("A", 32768)
	if !ShouldUseRichMessage(validRichText) {
		t.Errorf("text == 32768 runes SHOULD use rich message")
	}

	// Text > 32768 runes must route to cascade to prevent dropping content
	overLimitText := strings.Repeat("A", 32769)
	if ShouldUseRichMessage(overLimitText) {
		t.Errorf("text > 32768 runes should route to SplitHTMLChunks cascade to avoid losing content")
	}
}

func TestSendRichMessage_Success(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	richInput := InputRichMessage{
		Markdown: "# Article Title\n| Header1 | Header2 |\n|---|---|\n| Val1 | Val2 |",
		Thinking: &RichBlockThinking{
			Text:      "Internal CoT",
			Collapsed: true,
		},
	}

	msg, err := sendRichMessage(bot, 12345, richInput)
	if err != nil {
		t.Fatalf("sendRichMessage returned unexpected error: %v", err)
	}
	if msg.MessageID != 2125 {
		t.Errorf("expected message ID 2125, got %d", msg.MessageID)
	}

	mu.Lock()
	requests := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundRichCall := false
	for _, reqBody := range requests {
		vals, _ := url.ParseQuery(reqBody)
		if richRaw := vals.Get("rich_message"); richRaw != "" {
			foundRichCall = true
			if !strings.Contains(richRaw, "Val1") || !strings.Contains(richRaw, "Internal CoT") {
				t.Errorf("rich_message parameter missing expected payload: %s", richRaw)
			}
		}
	}
	if !foundRichCall {
		t.Errorf("mock server never received a request with rich_message parameter")
	}
}

func TestSendAdaptiveResponse_RichMessage_LongText(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	// 10,000 character long article without splitting
	longArticle := strings.Repeat("This is a detailed architectural overview of the system.\n", 180)
	if len([]rune(longArticle)) <= 4000 {
		t.Fatalf("test setup error: article length must exceed 4000, got %d", len([]rune(longArticle)))
	}

	chunks := sendAdaptiveResponse(bot, 12345, 0, longArticle)
	// Because sendRichMessage succeeds, it sends as a single monolithic message
	if len(chunks) != 1 {
		t.Errorf("expected sendAdaptiveResponse to send monolithic article as 1 chunk, got %d", len(chunks))
	}

	mu.Lock()
	requests := append([]string{}, *sentReqs...)
	mu.Unlock()

	richRequests := 0
	sendMessageRequests := 0
	for _, reqBody := range requests {
		vals, _ := url.ParseQuery(reqBody)
		if vals.Get("rich_message") != "" {
			richRequests++
		}
		if vals.Get("text") != "" {
			sendMessageRequests++
		}
	}

	if richRequests != 1 {
		t.Errorf("expected exactly 1 rich_message request, got %d", richRequests)
	}
	if sendMessageRequests != 0 {
		t.Errorf("expected 0 sendMessage requests when rich_message succeeds, got %d", sendMessageRequests)
	}
}

func TestSendAdaptiveResponse_GracefulDegradation_Fallback(t *testing.T) {
	var mu sync.Mutex
	var sentCalls []string

	// Mock server that returns 500 Internal Server Error for sendRichMessage,
	// but successfully handles sendMessage.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		sentCalls = append(sentCalls, r.URL.Path+"?"+string(bodyBytes))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"FallbackBot","username":"FallbackBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendRichMessage") {
			// Simulate Telegram API failure or unsupporting server
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error: rich message temporarily unavailable"}`))
			return
		}

		// Handle fallback sendMessage
		if strings.Contains(r.URL.Path, "sendMessage") {
			w.Write([]byte(`{"ok":true,"result":{"message_id":500,"chat":{"id":12345},"text":"sent via fallback"}}`))
			return
		}

		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	// Long text that triggers rich message, but fails at API level
	longText := strings.Repeat("Fallback test paragraph with detailed logs.\n\n", 120)
	chunks := sendAdaptiveResponse(bot, 12345, 0, longText)

	// Since sendRichMessage failed, fallback must have split into multiple chunks and sent each
	if len(chunks) <= 1 {
		t.Errorf("expected fallback to return multiple chunks, got %d", len(chunks))
	}

	mu.Lock()
	calls := append([]string{}, sentCalls...)
	mu.Unlock()

	richAttempted := false
	fallbackSends := 0
	for _, call := range calls {
		if strings.Contains(call, "sendRichMessage") {
			richAttempted = true
		}
		if strings.Contains(call, "sendMessage") {
			fallbackSends++
		}
	}

	if !richAttempted {
		t.Errorf("expected sendRichMessage to be attempted before fallback")
	}
	if fallbackSends < 2 {
		t.Errorf("expected at least 2 fallback sendMessage calls, got %d", fallbackSends)
	}
}

func TestSendAdaptiveResponse_ActiveMsgID_Cleanup(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	tableText := "Here are the metrics:\n| Metric | Value |\n|---|---|\n| QPS | 5000 |"
	activeID := 777

	chunks := sendAdaptiveResponse(bot, 12345, activeID, tableText)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for successful rich message, got %d", len(chunks))
	}

	mu.Lock()
	requests := append([]string{}, *sentReqs...)
	mu.Unlock()

	deletedActiveID := false
	for _, reqBody := range requests {
		vals, _ := url.ParseQuery(reqBody)
		if vals.Get("message_id") == "777" {
			deletedActiveID = true
		}
	}

	if !deletedActiveID {
		t.Errorf("expected active placeholder message 777 to be deleted after rich message success")
	}
}

func TestSendAdaptiveResponse_NilBot(t *testing.T) {
	longText := strings.Repeat("Testing nil bot execution safety.\n\n", 150)
	chunks := sendAdaptiveResponse(nil, 12345, 0, longText)
	if len(chunks) <= 1 {
		t.Errorf("expected chunks to be split safely when bot is nil, got %d", len(chunks))
	}
}

func TestSendChunk_RichMessageRouting(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	tableText := "# Table Overview\n| Col1 | Col2 |\n|---|---|\n| A | B |"
	chunks := sendChunk(bot, 12345, 0, tableText)
	if len(chunks) != 1 {
		t.Errorf("expected sendChunk with messageID=0 to route table through sendRichMessage (1 chunk), got %d", len(chunks))
	}

	mu.Lock()
	requests := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundRich := false
	for _, req := range requests {
		vals, _ := url.ParseQuery(req)
		if vals.Get("rich_message") != "" {
			foundRich = true
		}
	}
	if !foundRich {
		t.Errorf("expected sendChunk with messageID=0 and table to call sendRichMessage")
	}
}
