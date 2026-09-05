package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

func TestDispatchUpdate_IdleWorkerEviction(t *testing.T) {
	// Set a very short idle timeout for the test
	origTimeout := chatQueueIdleTimeout
	chatQueueIdleTimeout = 50 * time.Millisecond
	defer func() {
		chatQueueIdleTimeout = origTimeout
	}()

	testChatID := int64(888999)

	// Clean up map before test
	chatQueuesMu.Lock()
	delete(chatQueues, testChatID)
	chatQueuesMu.Unlock()

	// Dispatch dummy update with valid user
	upd := tgbotapi.Update{
		UpdateID: 101,
		Message: &tgbotapi.Message{
			MessageID: 202,
			Chat:      &tgbotapi.Chat{ID: testChatID},
			From:      &tgbotapi.User{ID: 12345, UserName: "testuser"},
			Text:      "/help",
		},
	}

	// Create in-memory DB
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	bot := &tgbotapi.BotAPI{}
	dispatchUpdate(bot, upd, db)

	// Verify worker was created in map
	chatQueuesMu.Lock()
	_, exists := chatQueues[testChatID]
	chatQueuesMu.Unlock()
	if !exists {
		t.Fatal("Expected chat queue worker to exist immediately after dispatch")
	}

	// Wait for idle timeout + buffer to let worker evict itself
	time.Sleep(120 * time.Millisecond)

	chatQueuesMu.Lock()
	_, stillExists := chatQueues[testChatID]
	chatQueuesMu.Unlock()

	if stillExists {
		t.Errorf("Expected chat queue worker for chatID %d to be evicted after idle timeout, but still in map", testChatID)
	}
}

func TestSplitOversizedParagraph_NeverSeversTagsOrEntities(t *testing.T) {
	// Construct a paragraph where an HTML tag crosses the boundary
	// Target chunk size: 50
	prefix := "This is a long introductory sentence before a link "
	tag := `<a href="https://example.com/a/very/long/target/url/path">Anchor Text</a>`
	suffix := " followed by trailing text."
	p := prefix + tag + suffix

	parts := splitOversizedParagraph(p, 50)
	if len(parts) < 2 {
		t.Fatalf("Expected multiple parts, got %d", len(parts))
	}

	for i, part := range parts {
		if len([]rune(part)) > 55 { // leeway for intact tag fallback
			t.Errorf("Part %d exceeds max chunk size: %d runes", i, len([]rune(part)))
		}
		// Verify no part starts with a broken tag attribute or ends with an unclosed opening bracket
		if strings.HasPrefix(part, `href=`) || strings.HasPrefix(part, `/a/very`) {
			t.Errorf("Part %d started with severed tag attribute: %q", i, part)
		}
		if strings.HasSuffix(part, `<a `) || strings.HasSuffix(part, `<a`) {
			t.Errorf("Part %d ended with severed tag opener: %q", i, part)
		}
	}
}

func TestSplitOversizedParagraph_EntityProtection(t *testing.T) {
	p := "Start text &amp; &quot; middle text &lt; &gt; end text"
	// Force cut around 15 runes
	parts := splitOversizedParagraph(p, 15)
	for i, part := range parts {
		if strings.HasSuffix(part, "&") || strings.HasSuffix(part, "&am") || strings.HasSuffix(part, "&qu") {
			t.Errorf("Part %d severed an entity: %q", i, part)
		}
	}
}

func TestSplitHTMLChunks_TagPreservationOnOversizedChunks(t *testing.T) {
	longText := "<pre><code class=\"language-go\">" +
		strings.Repeat("fmt.Println(\"Hello world line test!\")\n", 30) +
		"</code></pre>"

	chunks := SplitHTMLChunks(longText, 200)
	if len(chunks) <= 1 {
		t.Fatalf("Expected multiple chunks for large input, got %d", len(chunks))
	}

	for i, ch := range chunks {
		// Verify every chunk is valid HTML and tags are balanced
		if strings.Count(ch, "<pre>") != strings.Count(ch, "</pre>") {
			t.Errorf("Chunk %d has unbalanced <pre> tags:\n%s", i, ch)
		}
		if strings.Count(ch, "<code") != strings.Count(ch, "</code>") {
			t.Errorf("Chunk %d has unbalanced <code> tags:\n%s", i, ch)
		}
		// Verify no raw unescaped tag fragments
		if strings.Contains(ch, "&lt;code") || strings.Contains(ch, "&lt;pre") {
			t.Errorf("Chunk %d corrupted valid code/pre tags into escaped text:\n%s", i, ch)
		}
	}
}

func TestHandleCallbackQuery_ExpiredQuestionOption(t *testing.T) {
	// Given an expired/unknown callback data
	data := "ans_id:nonexistent_key_1234"
	opt, ok := getQuestionOption(data)
	if ok || opt != "" {
		t.Fatalf("Expected ok=false for nonexistent callback option, got ok=%v, opt=%s", ok, opt)
	}
}
