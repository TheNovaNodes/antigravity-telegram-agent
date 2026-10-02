package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

	// Case 6: Code containing <thought> must be preserved in markdown payload and NOT extracted to thinking
	text6 := "Explanation of tags:\n```xml\n<thought>code inside block</thought>\n```\nAlso inline `<thought>`."
	cleanMd6, thinking6 := ExtractThinkingAndMarkdown(text6)
	if thinking6 != nil {
		t.Errorf("expected thinking6 to be nil, got: %+v", thinking6)
	}
	if !strings.Contains(cleanMd6, "<thought>code inside block</thought>") {
		t.Errorf("expected fenced code with <thought> preserved, got %q", cleanMd6)
	}
	if !strings.Contains(cleanMd6, "`<thought>`") {
		t.Errorf("expected inline code `<thought>` preserved, got %q", cleanMd6)
	}

	// Case 7: Unclosed <thought> tag must not be extracted and must preserve full markdown
	text7 := "Some answer with <thought> unclosed tag and more response text."
	cleanMd7, thinking7 := ExtractThinkingAndMarkdown(text7)
	if thinking7 != nil {
		t.Errorf("expected thinking7 to be nil for unclosed tag, got: %+v", thinking7)
	}
	if cleanMd7 != text7 {
		t.Errorf("expected full text preserved without truncation, got %q", cleanMd7)
	}
}

func TestHasThoughts_CodeShieldingAndUnclosed(t *testing.T) {
	// 1. Unclosed tag -> false
	if HasThoughts("Some answer with <thought> unclosed tag") {
		t.Errorf("expected HasThoughts to return false for unclosed <thought>")
	}
	if HasThoughts("<think> unclosed") {
		t.Errorf("expected HasThoughts to return false for unclosed <think>")
	}

	// 2. Tags inside inline code -> false
	if HasThoughts("4. **Сворачиваемые цепочки рассуждений (`<thought>`)**") {
		t.Errorf("expected HasThoughts to return false for inline `<thought>`")
	}

	// 3. Tags inside fenced code -> false
	if HasThoughts("```xml\n<thought>code</thought>\n```") {
		t.Errorf("expected HasThoughts to return false for fenced `<thought>`")
	}
	if HasThoughts("```\n<think>code</think>\n```") {
		t.Errorf("expected HasThoughts to return false for fenced `<think>`")
	}

	// 4. Real thought tag outside code -> true
	if !HasThoughts("<thought>real thoughts</thought>") {
		t.Errorf("expected HasThoughts to return true for valid <thought>")
	}
	if !HasThoughts("```go\nx := 1\n```\n<think>real thoughts</think>") {
		t.Errorf("expected HasThoughts to return true when thought is outside code block")
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
	// Short text without table or thoughts (< 3000 runes)
	if ShouldUseRichMessage("Short message") {
		t.Errorf("short message without table should not use rich message")
	}

	// Border text just below threshold (2999 runes)
	borderBelow := strings.Repeat("A", 2999)
	if ShouldUseRichMessage(borderBelow) {
		t.Errorf("text == 2999 runes without table should use classic bubble")
	}

	// Border text at threshold (3000 runes)
	borderAt := strings.Repeat("A", 3000)
	if !ShouldUseRichMessage(borderAt) {
		t.Errorf("text == 3000 runes SHOULD use rich message (Tier 2 threshold)")
	}

	// Short text with table
	tableText := "Summary:\n| Col1 | Col2 |\n|---|---|\n| A | B |"
	if !ShouldUseRichMessage(tableText) {
		t.Errorf("short message with table SHOULD use rich message")
	}

	// Short text with thoughts
	thoughtText := "<thought>evaluating response</thought>Here is the answer."
	if !ShouldUseRichMessage(thoughtText) {
		t.Errorf("short message with thoughts SHOULD use rich message")
	}

	// Long text (> 3000 runes) without table
	longText := strings.Repeat("A", 4001)
	if !ShouldUseRichMessage(longText) {
		t.Errorf("text > 4000 runes SHOULD use rich message")
	}

	// Text at upper bound (32768 runes)
	validRichText := strings.Repeat("A", 32768)
	if !ShouldUseRichMessage(validRichText) {
		t.Errorf("text == 32768 runes SHOULD use rich message")
	}

	// Text > 32768 runes routes to Tier 3 (Markdown Artifact), so ShouldUseRichMessage is false
	overLimitText := strings.Repeat("A", 32769)
	if ShouldUseRichMessage(overLimitText) {
		t.Errorf("text > 32768 runes should route to Tier 3 artifact, not standard Tier 2 rich message")
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

func TestDelivery_Tier1_ClassicBubble(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	shortText := "Hello, this is a standard short response without tables or thoughts."
	if tier := DetermineDeliveryTier(shortText); tier != Tier1ClassicBubble {
		t.Fatalf("expected Tier1ClassicBubble, got %v", tier)
	}

	// 1. Case activeMsgID == 0 -> sendMessage
	mu.Lock()
	*sentReqs = nil
	mu.Unlock()

	chunks := sendAdaptiveResponse(bot, 12345, 0, shortText)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}

	mu.Lock()
	reqs := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundRich := false
	for _, req := range reqs {
		vals, _ := url.ParseQuery(req)
		if vals.Get("rich_message") != "" {
			foundRich = true
		}
	}
	if foundRich {
		t.Errorf("Tier 1 classic bubble should NEVER invoke sendRichMessage")
	}

	// 2. Case activeMsgID != 0 -> editMessageText
	mu.Lock()
	*sentReqs = nil
	mu.Unlock()

	chunksEdit := sendAdaptiveResponse(bot, 12345, 456, shortText)
	if len(chunksEdit) != 1 {
		t.Errorf("expected 1 chunk for edit, got %d", len(chunksEdit))
	}

	mu.Lock()
	reqsEdit := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundEdit := false
	for _, req := range reqsEdit {
		vals, _ := url.ParseQuery(req)
		if vals.Get("rich_message") != "" {
			t.Errorf("Tier 1 edit should NEVER invoke sendRichMessage")
		}
		if vals.Get("message_id") == "456" {
			foundEdit = true
		}
	}
	if !foundEdit {
		t.Errorf("expected activeMsgID 456 to be edited via editMessageText")
	}
}

func TestDelivery_Tier2_RichArticle(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	cases := []struct {
		name string
		text string
	}{
		{
			name: "Long text exceeding 3000 runes",
			text: strings.Repeat("Long article line for tier 2 test.\n", 100), // ~3500 runes
		},
		{
			name: "Short text with Markdown table",
			text: "Summary:\n| Metric | Value |\n|---|---|\n| Latency | 5ms |",
		},
		{
			name: "Short text with <thought> block",
			text: "<thought>Analyzing user intent</thought>Here is the analyzed plan.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tier := DetermineDeliveryTier(tc.text); tier != Tier2RichArticle {
				t.Fatalf("expected Tier2RichArticle, got %v", tier)
			}

			mu.Lock()
			*sentReqs = nil
			mu.Unlock()

			activeMsgID := 888
			chunks := sendAdaptiveResponse(bot, 12345, activeMsgID, tc.text)
			if len(chunks) != 1 {
				t.Errorf("expected 1 chunk, got %d", len(chunks))
			}

			mu.Lock()
			reqs := append([]string{}, *sentReqs...)
			mu.Unlock()

			foundRich := false
			deletedActiveID := false
			for _, req := range reqs {
				vals, _ := url.ParseQuery(req)
				if vals.Get("rich_message") != "" {
					foundRich = true
				}
				if vals.Get("message_id") == "888" {
					deletedActiveID = true
				}
			}

			if !foundRich {
				t.Errorf("expected sendRichMessage to be called for Tier 2 payload")
			}
			if !deletedActiveID {
				t.Errorf("expected activeMsgID 888 to be deleted after Tier 2 rich message success")
			}
		})
	}
}

func TestDelivery_Tier3_ExtremePayloadMarkdownArtifact(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	// Payload with > 32768 runes
	repeatLine := "This is line number with some extra padding to reach the artifact size limit.\n"
	payload := strings.Repeat(repeatLine, 450)
	totalRunes := utf8.RuneCountInString(payload)
	if totalRunes <= 32768 {
		t.Fatalf("payload must exceed 32768 runes, got %d", totalRunes)
	}

	if tier := DetermineDeliveryTier(payload); tier != Tier3MarkdownArtifact {
		t.Fatalf("expected Tier3MarkdownArtifact, got %v", tier)
	}

	mu.Lock()
	*sentReqs = nil
	mu.Unlock()

	activeMsgID := 999
	chunks := sendAdaptiveResponse(bot, 12345, activeMsgID, payload)
	if len(chunks) != 1 {
		t.Errorf("expected 1 preview chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "Полный ответ") {
		t.Errorf("expected notice in preview chunk, got: %s", chunks[0])
	}

	mu.Lock()
	reqs := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundRichPreview := false
	deletedActiveMsg := false
	foundDocument := false

	for _, req := range reqs {
		vals, _ := url.ParseQuery(req)
		if richRaw := vals.Get("rich_message"); richRaw != "" {
			foundRichPreview = true
			if !strings.Contains(richRaw, "Полный ответ") {
				t.Errorf("rich preview missing notice: %s", richRaw)
			}
		}
		if vals.Get("message_id") == "999" {
			deletedActiveMsg = true
		}
		if strings.Contains(req, "agent_response.md") {
			foundDocument = true
		}
	}

	if !foundRichPreview {
		t.Errorf("expected sendRichMessage preview for Tier 3 payload")
	}
	if !deletedActiveMsg {
		t.Errorf("expected activeMsgID 999 to be deleted on Tier 3")
	}
	if !foundDocument {
		t.Errorf("expected Telegram Document (agent_response.md) to be sent for Tier 3")
	}

	// Verify artifact file on disk
	artifactDir := getArtifactSaveDir()
	files, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatalf("failed to read artifact dir %s: %v", artifactDir, err)
	}
	var foundArtifact string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "response_") && strings.HasSuffix(f.Name(), ".md") {
			foundArtifact = filepath.Join(artifactDir, f.Name())
		}
	}
	if foundArtifact == "" {
		t.Fatalf("no response_<timestamp>.md artifact file found in %s", artifactDir)
	}
	defer os.Remove(foundArtifact)

	// Check permissions 0600
	fi, err := os.Stat(foundArtifact)
	if err != nil {
		t.Fatalf("failed to stat artifact file: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("expected file perm 0600, got %o", fi.Mode().Perm())
	}

	// Check content matches payload
	content, err := os.ReadFile(foundArtifact)
	if err != nil {
		t.Fatalf("failed to read artifact file: %v", err)
	}
	if string(content) != payload {
		t.Errorf("artifact content does not match payload! length got %d, expected %d", len(string(content)), len(payload))
	}
}

func TestDelivery_Tier3_MarkdownTruncationIntegrity(t *testing.T) {
	// Case 1: Text shorter than maxRunes remains untouched
	shortText := "Simple markdown text without overflowing limit."
	if truncated := TruncateMarkdownSafely(shortText, 2500); truncated != shortText {
		t.Errorf("expected untouched text for short input, got %s", truncated)
	}

	// Case 2: Plain text longer than limit, truncated cleanly without breaking runes
	longPlain := strings.Repeat("Абвгдеёжзийклмнопрстуфхцчшщъыьэюя ", 100)
	truncatedPlain := TruncateMarkdownSafely(longPlain, 500)
	if utf8.RuneCountInString(truncatedPlain) > 500 {
		t.Errorf("expected rune count <= 500, got %d", utf8.RuneCountInString(truncatedPlain))
	}
	if !utf8.ValidString(truncatedPlain) {
		t.Errorf("truncated string contains invalid UTF-8 sequences")
	}

	// Case 3: Truncation inside an open code block closes the fence
	codeBlockText := "Introduction\n\n```go\nfunc HeavyComputation() {\n" + strings.Repeat("\tfmt.Println(\"processing step\")\n", 50) + "}\n```\nConclusion text."
	truncatedCode := TruncateMarkdownSafely(codeBlockText, 200)
	if !strings.HasSuffix(truncatedCode, "```") {
		t.Errorf("expected truncated text inside code block to end with closing fence ```, got: %s", truncatedCode)
	}
	fenceCount := strings.Count(truncatedCode, "```")
	if fenceCount%2 != 0 {
		t.Errorf("expected even number of ``` fences (balanced), got %d: %s", fenceCount, truncatedCode)
	}

	// Case 4: Text with already closed code block before truncation limit
	closedCodeText := "Intro\n\n```go\nfunc A() {}\n```\n\n" + strings.Repeat("Subsequent paragraph text explaining the function. ", 40)
	truncatedClosed := TruncateMarkdownSafely(closedCodeText, 300)
	fenceCountClosed := strings.Count(truncatedClosed, "```")
	if fenceCountClosed != 2 {
		t.Errorf("expected exactly 2 fences for already closed code block, got %d", fenceCountClosed)
	}
}

func TestArtifactSaveDir_Isolation_NoHardcodedBotName(t *testing.T) {
	// 1. Verify default directory has NO hardcoded bot name
	defaultDir := getArtifactSaveDir()
	if strings.Contains(defaultDir, "trickster_gobot") {
		t.Errorf("expected getArtifactSaveDir() to have zero hardcoded bot names, got %s", defaultDir)
	}
	if !strings.HasSuffix(defaultDir, filepath.Join("scratch", "downloads")) {
		t.Errorf("expected defaultDir to end in scratch/downloads, got %s", defaultDir)
	}

	// 2. Verify custom session workspace isolation
	tmpWS, err := os.MkdirTemp("", "test_ws_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tmpWS)

	customDir := getArtifactSaveDir(tmpWS)
	expected := filepath.Join(tmpWS, "scratch", "downloads")
	if customDir != expected {
		t.Errorf("expected customDir %s, got %s", expected, customDir)
	}

	// Verify directory exists with 0700 permissions
	fi, err := os.Stat(customDir)
	if err != nil {
		t.Fatalf("failed to stat customDir: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("expected customDir to be a directory")
	}
	if fi.Mode().Perm() != 0700 {
		t.Errorf("expected 0700 permissions, got %o", fi.Mode().Perm())
	}
}

func TestDetermineDeliveryTier_HTMLLengthExpansion(t *testing.T) {
	// Construct markdown text with rune count < 3000 runes,
	// but containing entity-rich links expanding to > 4000 runes in HTML.
	text := strings.Repeat("Ref: <data> & [item](https://example.com/api?a=1&b=2&c=3&d=4&e=5&f=6) & <token> & results.\n", 30)
	markdownRunes := utf8.RuneCountInString(text)
	if markdownRunes >= RichMessageThreshold {
		t.Fatalf("test prerequisite failed: markdown length must be < %d, got %d", RichMessageThreshold, markdownRunes)
	}
	if HasMarkdownTable(text) {
		t.Fatalf("test prerequisite failed: text should not contain markdown table")
	}
	if HasThoughts(text) {
		t.Fatalf("test prerequisite failed: text should not contain thoughts")
	}

	html := MarkdownToTelegramHTML(text)
	htmlRunes := utf8.RuneCountInString(html)
	if htmlRunes <= ClassicMessageLimit {
		t.Fatalf("test prerequisite failed: html length must exceed %d, got %d", ClassicMessageLimit, htmlRunes)
	}

	// Because HTML exceeds ClassicMessageLimit, it MUST route to Tier2RichArticle to prevent SplitHTMLChunks cascade!
	tier := DetermineDeliveryTier(text)
	if tier != Tier2RichArticle {
		t.Errorf("expected Tier2RichArticle due to HTML length expansion (%d runes), got %v", htmlRunes, tier)
	}
}

func TestSendChunk_Tier3_Routing_MessageIDZero(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	// Payload > 32768 runes
	repeatLine := "Extreme payload line with some content for tier 3 routing via sendChunk.\n"
	payload := strings.Repeat(repeatLine, 500)
	totalRunes := utf8.RuneCountInString(payload)
	if totalRunes <= MaxRichMessageLength {
		t.Fatalf("payload must exceed %d, got %d", MaxRichMessageLength, totalRunes)
	}

	mu.Lock()
	*sentReqs = nil
	mu.Unlock()

	// Calling sendChunk with messageID == 0 must route to sendAdaptiveResponse (Tier 3)
	chunks := sendChunk(bot, 12345, 0, payload)
	if len(chunks) != 1 {
		t.Errorf("expected sendChunk to return 1 preview chunk for Tier 3, got %d", len(chunks))
	}

	mu.Lock()
	reqs := append([]string{}, *sentReqs...)
	mu.Unlock()

	foundRichPreview := false
	foundDocument := false

	for _, req := range reqs {
		vals, _ := url.ParseQuery(req)
		if richRaw := vals.Get("rich_message"); richRaw != "" {
			foundRichPreview = true
		}
		if strings.Contains(req, "agent_response.md") {
			foundDocument = true
		}
	}

	if !foundRichPreview {
		t.Errorf("expected sendChunk to invoke sendRichMessage preview for Tier 3 payload")
	}
	if !foundDocument {
		t.Errorf("expected sendChunk to dispatch agent_response.md document for Tier 3 payload")
	}
}

func TestRotateArtifactFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rotate_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	now := time.Now()
	// Create 25 mock response files
	for i := 0; i < 25; i++ {
		fileName := fmt.Sprintf("response_20260101_%04d.md", i)
		filePath := filepath.Join(tmpDir, fileName)
		if err := os.WriteFile(filePath, []byte("test artifact content"), 0600); err != nil {
			t.Fatalf("failed to write mock file: %v", err)
		}
		// Stagger mod times: older files have older timestamps
		modTime := now.Add(-time.Duration(25-i) * time.Minute)
		_ = os.Chtimes(filePath, modTime, modTime)
	}

	// 1. Rotate keeping max 10 files
	removed := RotateArtifactFiles(tmpDir, 10, 24*time.Hour)
	if removed != 15 {
		t.Errorf("expected 15 files to be removed, got %d", removed)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	if len(entries) != 10 {
		t.Errorf("expected 10 remaining files, got %d", len(entries))
	}

	// 2. Add an expired file older than 24 hours
	oldFile := filepath.Join(tmpDir, "response_old_expired.md")
	if err := os.WriteFile(oldFile, []byte("old content"), 0600); err != nil {
		t.Fatalf("failed to write old file: %v", err)
	}
	oldTime := now.Add(-48 * time.Hour)
	_ = os.Chtimes(oldFile, oldTime, oldTime)

	removedOld := RotateArtifactFiles(tmpDir, 10, 24*time.Hour)
	if removedOld < 1 {
		t.Errorf("expected at least 1 expired file removed, got %d", removedOld)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("expected expired file to be deleted")
	}
}
