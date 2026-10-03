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

func TestSendAdaptiveResponse_ActiveMsgID_InPlaceMorphing(t *testing.T) {
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

	editedWithRich := false
	deletedActiveID := false
	for _, reqBody := range requests {
		vals, _ := url.ParseQuery(reqBody)
		if vals.Get("rich_message") != "" && vals.Get("message_id") == "777" {
			editedWithRich = true
		}
		if vals.Get("rich_message") == "" && vals.Get("message_id") == "777" && vals.Get("text") == "" {
			deletedActiveID = true
		}
	}

	if !editedWithRich {
		t.Errorf("expected active placeholder message 777 to be morphed in-place via editMessageText with rich_message")
	}
	if deletedActiveID {
		t.Errorf("expected active placeholder message 777 NOT to be deleted (In-Place Morphing Guardrail)")
	}
}

func TestSendAdaptiveResponse_InPlaceMorphing_Fallback(t *testing.T) {
	var mu sync.Mutex
	var sentCalls []string

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

		// Fail editMessageText when rich_message is present
		if strings.Contains(r.URL.Path, "editMessageText") && strings.Contains(string(bodyBytes), "rich_message") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error: rich message edit unavailable"}`))
			return
		}

		// Succeed for classic editMessageText with text (HTML)
		if strings.Contains(r.URL.Path, "editMessageText") {
			w.Write([]byte(`{"ok":true,"result":{"message_id":777,"chat":{"id":12345},"text":"edited via html"}}`))
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

	activeID := 777
	tableText := "Here are the metrics:\n| Metric | Value |\n|---|---|\n| QPS | 5000 |"
	chunks := sendAdaptiveResponse(bot, 12345, activeID, tableText)
	if len(chunks) == 0 {
		t.Fatalf("expected non-empty chunks after fallback")
	}

	mu.Lock()
	calls := append([]string{}, sentCalls...)
	mu.Unlock()

	richEditAttempted := false
	classicEditSucceeded := false
	deletedCalled := false

	for _, call := range calls {
		if strings.Contains(call, "editMessageText") && strings.Contains(call, "rich_message") {
			richEditAttempted = true
		}
		if strings.Contains(call, "editMessageText") && !strings.Contains(call, "rich_message") {
			classicEditSucceeded = true
		}
		if strings.Contains(call, "deleteMessage") {
			deletedCalled = true
		}
	}

	if !richEditAttempted {
		t.Errorf("expected editRichMessage to be attempted first")
	}
	if !classicEditSucceeded {
		t.Errorf("expected fallback to classic HTML editMessageText to succeed")
	}
	if deletedCalled {
		t.Errorf("expected deleteMessage NEVER to be called during Tier 2 In-Place Morphing fallback")
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

			foundRichEdit := false
			deletedActiveID := false
			for _, req := range reqs {
				vals, _ := url.ParseQuery(req)
				if vals.Get("rich_message") != "" && vals.Get("message_id") == "888" {
					foundRichEdit = true
				}
				if vals.Get("rich_message") == "" && vals.Get("message_id") == "888" && vals.Get("text") == "" {
					deletedActiveID = true
				}
			}

			if !foundRichEdit {
				t.Errorf("expected editRichMessage to be called for Tier 2 payload with activeMsgID 888")
			}
			if deletedActiveID {
				t.Errorf("expected activeMsgID 888 NOT to be deleted after Tier 2 rich message success (In-Place Morphing Guardrail)")
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
	// Construct markdown text with rune count < 1500 runes,
	// but containing entity-rich text expanding to > 4000 runes in HTML.
	text := strings.Repeat("<&> ", 300)
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
	if HasMarkdownStructure(text) {
		t.Fatalf("test prerequisite failed: text should not contain markdown structure")
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

func TestHasMarkdownStructure(t *testing.T) {
	positiveCases := []struct {
		name string
		text string
	}{
		{"H1 heading", "# Architecture"},
		{"H2 heading", "## Overview"},
		{"H3 heading", "### Component"},
		{"H4 heading", "#### Details"},
		{"H5 heading", "##### Notes"},
		{"H6 heading", "###### Footnote"},
		{"Indented heading", "   ## Indented Section"},
		{"Fenced code block backticks", "```go\nfunc main() {}\n```"},
		{"Fenced code block tildes", "~~~json\n{\"ok\":true}\n~~~"},
		{"Blockquote single line", "> Important advice"},
		{"Blockquote empty line", ">"},
		{"Blockquote indented", "  > Note"},
		{"Bullet list hyphen", "- item A"},
		{"Bullet list asterisk", "* item B"},
		{"Bullet list plus", "+ item C"},
		{"Bullet list unicode bullet", "• item D"},
		{"Numbered list dot", "1. First step"},
		{"Numbered list parenthesis", "2) Second step"},
		{"Numbered list multi-digit", "100. Century step"},
		{"Horizontal rule dashes", "---"},
		{"Horizontal rule asterisks", "***"},
		{"Horizontal rule underscores", "___"},
		{"Horizontal rule spaced", "- - -"},
		{"Bold header plate asterisks", "**Executive Summary:** All systems normal"},
		{"Bold header plate underscores", "__Notice:__ Maintenance scheduled"},
		{"Markdown table", "| Col 1 | Col 2 |\n|---|---|\n| A | B |"},
	}

	for _, tc := range positiveCases {
		t.Run("Positive_"+tc.name, func(t *testing.T) {
			if !HasMarkdownStructure(tc.text) {
				t.Errorf("expected HasMarkdownStructure to return true for %q", tc.text)
			}
		})
	}

	negativeCases := []struct {
		name string
		text string
	}{
		{"Empty string", ""},
		{"Whitespace only", "   \n\t  \n"},
		{"Plain conversational reply", "Принято, задача выполнена."},
		{"Plain confirmation", "Файл сохранен в scratch."},
		{"Hashtag in plain text", "Please check #team channel for updates."},
		{"Math greater-than", "Value 5 > 3 is true."},
		{"Math multiply", "Formula 2 * 3 = 6."},
		{"Math minus", "Score is 10 - 2 = 8."},
		{"Float number", "The version is 3.14 released today."},
		{"Normal bold in middle", "This is normal text with a **bold** word inside."},
		{"Normal italic", "This is *italic* text."},
		{"Short dash", "--"},
	}

	for _, tc := range negativeCases {
		t.Run("Negative_"+tc.name, func(t *testing.T) {
			if HasMarkdownStructure(tc.text) {
				t.Errorf("expected HasMarkdownStructure to return false for %q", tc.text)
			}
		})
	}
}

func TestDetermineDeliveryTier_DoDMatrix(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		expectedTier DeliveryTier
	}{
		{
			name:         "Short flat text without markup -> Tier 1 (Classic Bubble)",
			text:         "Принято, задача выполнена в полном объеме.",
			expectedTier: Tier1ClassicBubble,
		},
		{
			name:         "Flat text of 1600 runes without markdown structure -> Tier 1 (Classic Bubble)",
			text:         strings.Repeat("Привет мир это обычный плоский текст без разметки. ", 32), // ~1630 runes
			expectedTier: Tier1ClassicBubble,
		},
		{
			name:         "Response with H2 heading of 1600 runes -> Tier 2 (Rich Article)",
			text:         "## Архитектурный план реализации\n\n" + strings.Repeat("Подробное описание этапа инженерного плана. ", 36), // ~1620 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Response with H3 heading of 1600 runes -> Tier 2 (Rich Article)",
			text:         "### Детализация микросервиса\n\n" + strings.Repeat("Описание работы подсистемы и протокола обмена. ", 35), // ~1610 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Response with task list of 1800 runes -> Tier 2 (Rich Article)",
			text:         "- Задача 1: Первичная диагностика системы\n" + strings.Repeat("- Подзадача: выполнение проверки и сбор метрик.\n", 37), // ~1800 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Response with numbered list of 1800 runes -> Tier 2 (Rich Article)",
			text:         "1. Первый этап развертывания кластера в проде\n" + strings.Repeat("2. Следующий этап с проверкой репликации данных.\n", 37), // ~1800 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Response with code block -> Tier 2 (Rich Article)",
			text:         "```go\nfunc ProcessTelemetry() error {\n" + strings.Repeat("\t// trace telemetry packet and check headers\n", 40) + "\treturn nil\n}\n```", // ~1800 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Response with tilde code block -> Tier 2 (Rich Article)",
			text:         "~~~bash\n" + strings.Repeat("echo 'deploying isolated container to cluster'\n", 40) + "~~~", // ~1800 runes
			expectedTier: Tier2RichArticle,
		},
		{
			name:         "Message > 32KB -> Tier 3 (Markdown Artifact)",
			text:         strings.Repeat("Строка отчета телеметрии для артефакта.\n", 900), // ~36,000 runes
			expectedTier: Tier3MarkdownArtifact,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier := DetermineDeliveryTier(tc.text)
			if tier != tc.expectedTier {
				t.Errorf("expected %v, got %v (runes: %d)", tc.expectedTier, tier, utf8.RuneCountInString(tc.text))
			}
		})
	}
}

func BenchmarkHasMarkdownStructure(b *testing.B) {
	text := "## Architecture Overview\n\nThis is a long analytical response.\n\n- Point 1\n- Point 2\n\n```go\nfunc Run() {}\n```\n"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = HasMarkdownStructure(text)
	}
}

func TestExtractRichMessageText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Structured markdown only",
			input:    `{"markdown": "## Executive Summary\nSystem active"}`,
			expected: "## Executive Summary\nSystem active",
		},
		{
			name:     "Structured text only",
			input:    `{"text": "Simple plain text inside rich object"}`,
			expected: "Simple plain text inside rich object",
		},
		{
			name:     "Markdown preferred over text",
			input:    `{"markdown": "### Formatted Heading", "text": "Unformatted fallback"}`,
			expected: "### Formatted Heading",
		},
		{
			name:     "Thinking only fallback",
			input:    `{"thinking": {"text": "Reasoning trace without final markdown"}}`,
			expected: "Reasoning trace without final markdown",
		},
		{
			name:     "Thinking plus markdown returns markdown",
			input:    `{"markdown": "Final Answer", "thinking": {"text": "Internal thoughts"}}`,
			expected: "Final Answer",
		},
		{
			name:     "Direct JSON string primitive",
			input:    `"Direct rich message string literal"`,
			expected: "Direct rich message string literal",
		},
		{
			name:     "Empty raw JSON string",
			input:    `""`,
			expected: "",
		},
		{
			name:     "Null literal",
			input:    `null`,
			expected: "",
		},
		{
			name:     "Empty object",
			input:    `{}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ExtractRichMessageText(json.RawMessage(tt.input))
			if actual != tt.expected {
				t.Errorf("ExtractRichMessageText(%s) = %q; want %q", tt.input, actual, tt.expected)
			}
		})
	}
}

func TestAttachAndGetAttachedRichMessage(t *testing.T) {
	// 1. Message with empty initial Text
	msgEmpty := &tgbotapi.Message{
		MessageID: 10,
		Chat:      &tgbotapi.Chat{ID: 12345},
	}
	AttachRichMessage(msgEmpty, "Attached markdown content")
	if msgEmpty.Text != "Attached markdown content" {
		t.Errorf("expected msgEmpty.Text to be populated, got %q", msgEmpty.Text)
	}
	retrieved, ok := GetAttachedRichMessage(msgEmpty)
	if !ok || retrieved != "Attached markdown content" {
		t.Errorf("expected GetAttachedRichMessage to return %q, got %q (ok=%v)", "Attached markdown content", retrieved, ok)
	}

	// 2. Message with existing initial Text preserved
	msgWithText := &tgbotapi.Message{
		MessageID: 11,
		Chat:      &tgbotapi.Chat{ID: 12345},
		Text:      "Initial user command",
	}
	AttachRichMessage(msgWithText, "Alternative rich content")
	if msgWithText.Text != "Initial user command" {
		t.Errorf("expected existing text to be preserved, got %q", msgWithText.Text)
	}
	retrievedWithText, ok := GetAttachedRichMessage(msgWithText)
	if !ok || retrievedWithText != "Alternative rich content" {
		t.Errorf("expected GetAttachedRichMessage to return %q, got %q", "Alternative rich content", retrievedWithText)
	}

	// 3. Nil message safe handling
	AttachRichMessage(nil, "noop")
	nilRetrieved, nilOk := GetAttachedRichMessage(nil)
	if nilOk || nilRetrieved != "" {
		t.Errorf("expected nil message to return empty, got %q (ok=%v)", nilRetrieved, nilOk)
	}

	// 4. Bounded capacity eviction
	origCap := maxAttachedRichCap
	maxAttachedRichCap = 5
	defer func() { maxAttachedRichCap = origCap }()

	var msgs []*tgbotapi.Message
	for i := 0; i < 10; i++ {
		m := &tgbotapi.Message{MessageID: 100 + i}
		AttachRichMessage(m, fmt.Sprintf("payload_%d", i))
		msgs = append(msgs, m)
	}

	// Oldest entries (e.g. msg 0) should be evicted from the attachedRich map
	_, okOld := GetAttachedRichMessage(msgs[0])
	if okOld {
		t.Errorf("expected oldest message to be evicted from bounded cache")
	}
	// Newest entry should still exist
	valNew, okNew := GetAttachedRichMessage(msgs[9])
	if !okNew || valNew != "payload_9" {
		t.Errorf("expected newest message to exist in cache, got %q (ok=%v)", valNew, okNew)
	}
}

func TestParseMessageFromJSON_RichMessageVariants(t *testing.T) {
	// Direct message with rich_message
	rawDirect := []byte(`{
		"message_id": 201,
		"chat": {"id": 555},
		"from": {"id": 111, "first_name": "TestBot"},
		"rich_message": {
			"markdown": "# Architecture Blueprint\nPhase 1 complete."
		}
	}`)
	msg, err := ParseMessageFromJSON(rawDirect)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON failed: %v", err)
	}
	if msg.MessageID != 201 {
		t.Errorf("expected message_id 201, got %d", msg.MessageID)
	}
	if msg.Text != "# Architecture Blueprint\nPhase 1 complete." {
		t.Errorf("expected enriched text, got %q", msg.Text)
	}

	// Forwarded message with forward_origin containing rich_message
	rawForwarded := []byte(`{
		"message_id": 202,
		"chat": {"id": 555},
		"from": {"id": 222},
		"forward_origin": {
			"type": "user",
			"rich_message": {
				"markdown": "Forwarded agent task report"
			}
		}
	}`)
	msgFwd, err := ParseMessageFromJSON(rawForwarded)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON forward_origin failed: %v", err)
	}
	if msgFwd.Text != "Forwarded agent task report" {
		t.Errorf("expected forwarded rich text, got %q", msgFwd.Text)
	}

	// Forwarded message with forward_from containing rich_message
	rawFwdFrom := []byte(`{
		"message_id": 203,
		"chat": {"id": 555},
		"from": {"id": 222},
		"forward_from": {
			"id": 999,
			"rich_message": {
				"markdown": "Forwarded from subagent"
			}
		}
	}`)
	msgFwdFrom, err := ParseMessageFromJSON(rawFwdFrom)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON forward_from failed: %v", err)
	}
	if msgFwdFrom.Text != "Forwarded from subagent" {
		t.Errorf("expected forward_from rich text, got %q", msgFwdFrom.Text)
	}

	// Message with reply_to_message containing rich_message
	rawReply := []byte(`{
		"message_id": 204,
		"chat": {"id": 555},
		"from": {"id": 222},
		"text": "Please summarize this",
		"reply_to_message": {
			"message_id": 200,
			"chat": {"id": 555},
			"rich_message": {
				"markdown": "Original long rich response to summarize"
			}
		}
	}`)
	msgReply, err := ParseMessageFromJSON(rawReply)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON reply failed: %v", err)
	}
	if msgReply.Text != "Please summarize this" {
		t.Errorf("expected prompt text preserved, got %q", msgReply.Text)
	}
	if msgReply.ReplyToMessage == nil || msgReply.ReplyToMessage.Text != "Original long rich response to summarize" {
		t.Errorf("expected reply_to_message.Text to be enriched, got %+v", msgReply.ReplyToMessage)
	}
}

func TestParseUpdatesFromJSON_WrappedAndRaw(t *testing.T) {
	// Raw JSON array of updates
	rawUpdates := []byte(`[
		{
			"update_id": 7001,
			"message": {
				"message_id": 1,
				"chat": {"id": 999},
				"from": {"id": 888},
				"rich_message": {
					"markdown": "Update 1 rich text"
				}
			}
		},
		{
			"update_id": 7002,
			"message": {
				"message_id": 2,
				"chat": {"id": 999},
				"from": {"id": 888},
				"text": "Standard text update"
			}
		}
	]`)

	updates, err := ParseUpdatesFromJSON(rawUpdates)
	if err != nil {
		t.Fatalf("ParseUpdatesFromJSON failed: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if updates[0].Message.Text != "Update 1 rich text" {
		t.Errorf("expected update 0 rich text, got %q", updates[0].Message.Text)
	}
	if updates[1].Message.Text != "Standard text update" {
		t.Errorf("expected update 1 standard text, got %q", updates[1].Message.Text)
	}

	// Wrapped in Telegram APIResponse {"ok": true, "result": [...]}
	wrappedUpdates := []byte(`{
		"ok": true,
		"result": [
			{
				"update_id": 7003,
				"message": {
					"message_id": 3,
					"chat": {"id": 999},
					"from": {"id": 888},
					"rich_message": "String literal rich message"
				}
			}
		]
	}`)
	wrappedRes, err := ParseUpdatesFromJSON(wrappedUpdates)
	if err != nil {
		t.Fatalf("ParseUpdatesFromJSON wrapped failed: %v", err)
	}
	if len(wrappedRes) != 1 || wrappedRes[0].Message.Text != "String literal rich message" {
		t.Errorf("unexpected wrapped update result: %+v", wrappedRes)
	}
}

func TestExtractRichMessageText_TelegramBotAPI_BlocksAST(t *testing.T) {
	// Exact payload from Issue #366 incident report
	rawIssuePayload := []byte(`{
		"blocks": [
			{
				"type": "heading",
				"size": 1,
				"text": "Header 1"
			},
			{
				"type": "paragraph",
				"text": [
					"This is a paragraph with ",
					{"type": "bold", "text": "bold"},
					" and ",
					{"type": "italic", "text": "italic"},
					"."
				]
			},
			{
				"type": "list",
				"items": [
					{
						"label": "•",
						"blocks": [{"type": "paragraph", "text": "List item 1"}]
					}
				]
			},
			{
				"type": "pre",
				"language": "go",
				"text": "func main() {}"
			}
		]
	}`)

	actual := ExtractRichMessageText(rawIssuePayload)
	expected := "# Header 1\n\nThis is a paragraph with **bold** and *italic*.\n\n- List item 1\n\n```go\nfunc main() {}\n```"

	if actual != expected {
		t.Errorf("ExtractRichMessageText(rawIssuePayload) mismatched!\nGot:\n%s\n\nWant:\n%s", actual, expected)
	}
}

func TestExtractRichMessageText_BlocksAST_AdvancedNodes(t *testing.T) {
	// Tests covering heading sizes, quotes, tables, inline links, code, strikethrough, spoilers
	rawComplex := []byte(`{
		"blocks": [
			{
				"type": "heading",
				"size": 3,
				"text": [
					"Sub-section with ",
					{"type": "code", "text": "telemetry()"}
				]
			},
			{
				"type": "quote",
				"text": "Multi-line quote block\nLine 2 of quote"
			},
			{
				"type": "paragraph",
				"text": [
					"Check ",
					{"type": "link", "url": "https://thenovanodes.com", "text": "NovaNodes"},
					" or ",
					{"type": "strike", "text": "deprecated"},
					" or ",
					{"type": "spoiler", "text": "secret-token"},
					"."
				]
			},
			{
				"type": "table",
				"header": ["Key", "Status"],
				"rows": [
					["Daemon", "Active"],
					["Cluster", "Healthy"]
				]
			},
			{
				"type": "list",
				"items": [
					{
						"label": "1.",
						"text": "Step one"
					},
					{
						"label": "2.",
						"text": "Step two"
					}
				]
			},
			{
				"type": "unknown_future_block",
				"text": "Graceful fallback text"
			}
		]
	}`)

	actual := ExtractRichMessageText(rawComplex)

	// Assertions on reconstructed elements
	if !strings.Contains(actual, "### Sub-section with `telemetry()`") {
		t.Errorf("expected level 3 heading with inline code, got:\n%s", actual)
	}
	if !strings.Contains(actual, "> Multi-line quote block\n> Line 2 of quote") {
		t.Errorf("expected blockquote with > prefix, got:\n%s", actual)
	}
	if !strings.Contains(actual, "[NovaNodes](https://thenovanodes.com)") {
		t.Errorf("expected markdown link, got:\n%s", actual)
	}
	if !strings.Contains(actual, "~~deprecated~~") {
		t.Errorf("expected strikethrough, got:\n%s", actual)
	}
	if !strings.Contains(actual, "||secret-token||") {
		t.Errorf("expected spoiler, got:\n%s", actual)
	}
	if !strings.Contains(actual, "| Key | Status |\n| --- | --- |\n| Daemon | Active |\n| Cluster | Healthy |") {
		t.Errorf("expected markdown table, got:\n%s", actual)
	}
	if !strings.Contains(actual, "1. Step one\n2. Step two") {
		t.Errorf("expected ordered list items, got:\n%s", actual)
	}
	if !strings.Contains(actual, "Graceful fallback text") {
		t.Errorf("expected fallback for unknown block type, got:\n%s", actual)
	}
}

func TestParseMessageFromJSON_BlocksAST(t *testing.T) {
	rawMsg := []byte(`{
		"message_id": 888,
		"chat": {"id": 12345},
		"from": {"id": 777},
		"rich_message": {
			"blocks": [
				{
					"type": "heading",
					"size": 2,
					"text": "Cluster Status"
				},
				{
					"type": "paragraph",
					"text": "All 9 Pure Go bots operational."
				}
			]
		}
	}`)

	msg, err := ParseMessageFromJSON(rawMsg)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON failed: %v", err)
	}
	expected := "## Cluster Status\n\nAll 9 Pure Go bots operational."
	if msg.Text != expected {
		t.Errorf("expected msg.Text %q, got %q", expected, msg.Text)
	}
}
