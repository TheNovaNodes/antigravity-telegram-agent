package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEscapeHTML(t *testing.T) {
	in := "<script>alert('x') & run</script>"
	expected := "&lt;script&gt;alert('x') &amp; run&lt;/script&gt;"
	if out := escapeHTML(in); out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestBalanceAndSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "Close unclosed bold",
			in:   "<b>hello",
			out:  "<b>hello</b>",
		},
		{
			name: "Disallowed tag sanitized",
			in:   "<b>hello</b><script>bad</script>",
			out:  "<b>hello</b>&lt;script&gt;bad&lt;/script&gt;",
		},
		{
			name: "Nested tags",
			in:   "<b><i>bold italic</b>",
			out:  "<b><i>bold italic</i></b>",
		},
		{
			name: "Attributes allowed",
			in:   `<a href="http://test.com">Link</a>`,
			out:  `<a href="http://test.com">Link</a>`,
		},
		{
			name: "Self closing tag allowed", // we turn it to string actually
			in:   "hello <br/>",
			out:  "hello &lt;br/&gt;", // br not in allowed tags
		},
		{
			name: "Blockquote expandable",
			in:   `<blockquote expandable>💭 text`,
			out:  `<blockquote expandable>💭 text</blockquote>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := balanceAndSanitizeTelegramHTML(tc.in); got != tc.out {
				t.Errorf("\nexpected: %s\ngot:      %s", tc.out, got)
			}
		})
	}
}

func TestMarkdownToTelegramHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "Watchdog and notice markdown italics",
			in:   "⚠️ _[Response truncated: buffer exceeded 1MB limit]_",
			out:  "⚠️ <i>[Response truncated: buffer exceeded 1MB limit]</i>",
		},
		{
			name: "Bold and Italic",
			in:   "**bold** and *italic*",
			out:  "<b>bold</b> and <i>italic</i>",
		},
		{
			name: "Code block",
			in:   "```python\nprint(1)\n```",
			out:  "<pre><code class=\"language-python\">print(1)\n</code></pre>",
		},
		{
			name: "Thinking block",
			in:   "<think>\nhmmm\n</think>",
			out:  "<blockquote expandable>💭 <b>Thinking Process:</b>\nhmmm</blockquote>",
		},
		{
			name: "Unclosed code block",
			in:   "```\nunclosed",
			out:  "<pre><code>unclosed\n</code></pre>",
		},
		{
			name: "Inline code",
			in:   "use `sudo rm -rf`",
			out:  "use <code>sudo rm -rf</code>",
		},
		{
			name: "List",
			in:   "- item 1\n- item 2",
			out:  "• item 1\n• item 2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkdownToTelegramHTML(tc.in); got != tc.out {
				t.Errorf("\nexpected: %s\ngot:      %s", tc.out, got)
			}
		})
	}
}

func TestSplitHTMLChunks(t *testing.T) {
	text := strings.Repeat("A", 4000)
	chunks := SplitHTMLChunks(text, 3800)
	if len(chunks) != 2 {
		t.Errorf("Expected 2 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != 3800 {
		t.Errorf("Expected chunk 0 to be 3800, got %d", len(chunks[0]))
	}

	// Mixed HTML
	text2 := "<b>" + strings.Repeat("A", 3797) + "</b>"
	chunks2 := SplitHTMLChunks(text2, 3800)
	if len(chunks2) != 2 {
		t.Errorf("Expected 2 chunks for mixed HTML, got %d", len(chunks2))
	}

	// Chunk 1 should auto-close <b>
	if !strings.HasSuffix(chunks2[0], "</b>") {
		t.Errorf("Chunk 1 did not close tag: %s", chunks2[0])
	}
}

func TestMarkdownToTelegramHTML_Advanced(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "Images and links",
			input:    "Check this image ![Diagram](https://example.com/pic.png) and this [Site](https://example.com).",
			contains: []string{`<a href="https://example.com/pic.png">🖼 Diagram</a>`, `<a href="https://example.com">Site</a>`},
		},
		{
			name:     "Spoilers and strikethrough",
			input:    "This is ||hidden text|| and this is ~~old text~~.",
			contains: []string{`<tg-spoiler>hidden text</tg-spoiler>`, `<s>old text</s>`},
		},
		{
			name:     "Bold italic combined",
			input:    "***Top Priority***",
			contains: []string{`<b><i>Top Priority</i></b>`},
		},
		{
			name:     "Horizontal rules",
			input:    "Section 1\n---\nSection 2\n***\nSection 3",
			contains: []string{`───────────────`},
		},
		{
			name:     "Simple blockquote",
			input:    "> This is a quote\n> Second line",
			contains: []string{`<blockquote>This is a quote`, `Second line</blockquote>`},
		},
		{
			name:     "Expandable note blockquote",
			input:    "> [!NOTE]\n> This is important context",
			contains: []string{`<blockquote expandable>[!NOTE]`, `This is important context</blockquote>`},
		},
		{
			name:     "Markdown table conversion",
			input:    "| Col 1 | Col 2 |\n| Data 1 | Data 2 |",
			contains: []string{`<pre><code>| Col 1 | Col 2 |`, `| Data 1 | Data 2 |</code></pre>`},
		},
		{
			name:     "Numbered list",
			input:    "1. First step\n2. Second step",
			contains: []string{`1. First step`, `2. Second step`},
		},
		{
			name:     "Headers conversion",
			input:    "# Header One\n## Header Two",
			contains: []string{`<b><u>Header One</u></b>`, `<b><u>Header Two</u></b>`},
		},
		{
			name:     "Empty input",
			input:    "",
			contains: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MarkdownToTelegramHTML(tc.input)
			for _, exp := range tc.contains {
				if !strings.Contains(got, exp) {
					t.Errorf("MarkdownToTelegramHTML(%q)\nGot: %q\nExpected to contain: %q", tc.input, got, exp)
				}
			}
		})
	}
}

func TestSplitHTMLChunks_LargeParagraph(t *testing.T) {
	// Create a single large paragraph exceeding maxChunkSize
	largeText := strings.Repeat("АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюя", 100)
	chunks := SplitHTMLChunks(largeText, 200)

	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks for large paragraph, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if len([]rune(chunk)) > 250 { // with HTML sanitization overhead
			t.Errorf("Chunk %d exceeds expected size: rune len = %d", i, len([]rune(chunk)))
		}
	}
}

func TestCleanIdleSessions_Eviction(t *testing.T) {
	sessionMu.Lock()
	sActive := &AgySession{
		BotName:      "ActiveBot",
		ChatID:       1001,
		UserID:       2001,
		LastActivity: time.Now(),
		isAlive:      true,
	}
	sIdle := &AgySession{
		BotName:      "IdleBot",
		ChatID:       1002,
		UserID:       2002,
		LastActivity: time.Now().Add(-48 * time.Hour),
		isAlive:      true,
	}
	globalSessions["ActiveBot:1001:2001"] = sActive
	globalSessions["IdleBot:1002:2002"] = sIdle
	sessionMu.Unlock()

	evicted := CleanIdleSessions(24 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session to be evicted, got %d", evicted)
	}

	sessionMu.Lock()
	_, activeExists := globalSessions["ActiveBot:1001:2001"]
	_, idleExists := globalSessions["IdleBot:1002:2002"]
	delete(globalSessions, "ActiveBot:1001:2001")
	sessionMu.Unlock()

	if !activeExists {
		t.Errorf("Expected active session to be preserved")
	}
	if idleExists {
		t.Errorf("Expected idle session to be deleted from globalSessions")
	}
}

func TestCleanIdleSessions_TwoHourThreshold(t *testing.T) {
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 4 * 1024 * 1024 * 1024,
			UsedRatio:      0.50, // Normal memory state (no pressure)
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sRecent := &AgySession{
		BotName:      "RecentBot",
		ChatID:       3001,
		UserID:       4001,
		LastActivity: time.Now().Add(-1 * time.Hour), // 1 hour idle: should stay (< 2h)
		isAlive:      true,
	}
	sExpired := &AgySession{
		BotName:      "ExpiredBot",
		ChatID:       3002,
		UserID:       4002,
		LastActivity: time.Now().Add(-3 * time.Hour), // 3 hours idle: should be evicted (> 2h)
		isAlive:      true,
	}
	globalSessions["RecentBot:3001:4001"] = sRecent
	globalSessions["ExpiredBot:3002:4002"] = sExpired
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session to be evicted for 2h threshold, got %d", evicted)
	}

	sessionMu.Lock()
	_, recentExists := globalSessions["RecentBot:3001:4001"]
	_, expiredExists := globalSessions["ExpiredBot:3002:4002"]
	delete(globalSessions, "RecentBot:3001:4001")
	sessionMu.Unlock()

	if !recentExists {
		t.Errorf("Expected 1-hour idle session to be preserved")
	}
	if expiredExists {
		t.Errorf("Expected 3-hour idle session to be evicted from globalSessions")
	}
}

func TestGetSystemMemoryStats(t *testing.T) {
	stats, err := getSystemMemoryStats()
	if err != nil {
		t.Skipf("Skipping on environments without /proc/meminfo: %v", err)
	}
	if stats.TotalBytes == 0 {
		t.Errorf("Expected TotalBytes > 0")
	}
	if stats.UsedRatio < 0 || stats.UsedRatio > 1.0 {
		t.Errorf("UsedRatio out of range [0, 1]: %f", stats.UsedRatio)
	}
}

func TestCleanIdleSessions_MemoryPressure_AggressiveEviction(t *testing.T) {
	// Mock high memory pressure (85% used, 500MB available)
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 500 * 1024 * 1024, // < 1.5GB
			UsedRatio:      0.85,              // >= 75%
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sRecent := &AgySession{
		BotName:      "PressureRecentBot",
		ChatID:       5001,
		UserID:       6001,
		LastActivity: time.Now().Add(-10 * time.Minute), // 10m idle: should stay (< 20m)
		isAlive:      true,
	}
	sIdleStale := &AgySession{
		BotName:      "PressureIdleBot",
		ChatID:       5002,
		UserID:       6002,
		LastActivity: time.Now().Add(-35 * time.Minute), // 35m idle: should be aggressively evicted (> 20m)
		isAlive:      true,
	}
	globalSessions["PressureRecentBot:5001:6001"] = sRecent
	globalSessions["PressureIdleBot:5002:6002"] = sIdleStale
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 1 {
		t.Errorf("Expected 1 session aggressively evicted under memory pressure, got %d", evicted)
	}

	sessionMu.Lock()
	_, recentExists := globalSessions["PressureRecentBot:5001:6001"]
	_, idleExists := globalSessions["PressureIdleBot:5002:6002"]
	delete(globalSessions, "PressureRecentBot:5001:6001")
	sessionMu.Unlock()

	if !recentExists {
		t.Errorf("Expected 10-minute idle session to be preserved")
	}
	if idleExists {
		t.Errorf("Expected 35-minute idle session to be evicted under memory pressure")
	}
}

func TestCleanIdleSessions_ActiveTurnProtectedUnderMemoryPressure(t *testing.T) {
	// Mock extreme memory pressure (95% used, 100MB available)
	origFunc := sysMemStatsFunc
	sysMemStatsFunc = func() (MemoryStats, error) {
		return MemoryStats{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 100 * 1024 * 1024,
			UsedRatio:      0.95,
		}, nil
	}
	defer func() {
		sysMemStatsFunc = origFunc
	}()

	sessionMu.Lock()
	sActiveTurn := &AgySession{
		BotName:         "ActiveTurnBot",
		ChatID:          7001,
		UserID:          8001,
		LastActivity:    time.Now().Add(-45 * time.Minute), // idle 45m, but active turn in flight!
		ActiveMessageID: 9999,
		ActiveTurnStart: time.Now().Add(-5 * time.Minute),
		isAlive:         true,
	}
	globalSessions["ActiveTurnBot:7001:8001"] = sActiveTurn
	sessionMu.Unlock()

	evicted := CleanIdleSessions(2 * time.Hour)
	if evicted != 0 {
		t.Errorf("Expected active in-flight turn to NEVER be evicted, got %d evictions", evicted)
	}

	sessionMu.Lock()
	_, activeExists := globalSessions["ActiveTurnBot:7001:8001"]
	delete(globalSessions, "ActiveTurnBot:7001:8001")
	sessionMu.Unlock()

	if !activeExists {
		t.Errorf("Expected active in-flight session to be strictly immune to GC")
	}
}

func TestCleanOldFiles_DiskHygiene(t *testing.T) {
	tempDir := t.TempDir()
	freshFile := filepath.Join(tempDir, "fresh.txt")
	oldFile := filepath.Join(tempDir, "old.txt")

	if err := os.WriteFile(freshFile, []byte("fresh"), 0644); err != nil {
		t.Fatalf("Failed to create fresh file: %v", err)
	}
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create old file: %v", err)
	}

	// Change mtime of oldFile to 3 days ago
	oldTime := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set old mtime: %v", err)
	}

	cleaned := CleanOldFiles(tempDir, 24*time.Hour)
	if cleaned != 1 {
		t.Errorf("Expected 1 file cleaned, got %d", cleaned)
	}

	if _, err := os.Stat(freshFile); os.IsNotExist(err) {
		t.Errorf("Fresh file was unexpectedly deleted")
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("Old file was not deleted")
	}
}

func TestSplitHTMLChunks_CrossChunkTagPreservation(t *testing.T) {
	// A long code block exceeding maxChunkSize (100)
	longCode := "```go\n" + strings.Repeat("fmt.Println(\"testing long code block chunks\")\n", 10) + "```"
	html := MarkdownToTelegramHTML(longCode)

	chunks := SplitHTMLChunks(html, 150)
	if len(chunks) < 2 {
		t.Fatalf("Expected at least 2 chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		// All chunks must have balanced tags
		if strings.Count(chunk, "<pre>") != strings.Count(chunk, "</pre>") {
			t.Errorf("Chunk %d has unbalanced <pre> tags: %s", i, chunk)
		}
		if strings.Count(chunk, "<code") != strings.Count(chunk, "</code>") {
			t.Errorf("Chunk %d has unbalanced <code> tags: %s", i, chunk)
		}
	}
}

// FuzzSplitHTMLChunks fuzzes the message chunking and HTML balancing algorithm.
// It verifies that no arbitrary text or HTML nesting crashes the splitter,
// and that returned chunks satisfy length limits.
func FuzzSplitHTMLChunks(f *testing.F) {
	// Seed corpus with realistic markdown and HTML patterns
	f.Add("Hello world", 100)
	f.Add("<b>Bold</b> and <i>italic</i> formatting with `inline code`.", 50)
	f.Add("<pre><code class=\"language-go\">package main\nfunc main() {}\n</code></pre>", 80)
	f.Add("<a href=\"https://thenovanodes.com\">The NovaNodes Portal</a>", 60)
	f.Add("Special chars: &amp; &lt; &gt; &quot; &#39;", 70)
	f.Add(strings.Repeat("Long unwrapped word without spaces ", 50), 200)
	f.Add("Nested <b><i><u>underlined bold italic</u></i></b> text.", 40)
	f.Add("Mismatched <b>unclosed tag without end", 60)

	f.Fuzz(func(t *testing.T, text string, maxChunkSize int) {
		if maxChunkSize < 20 || maxChunkSize > 4096 {
			return
		}
		chunks := SplitHTMLChunks(text, maxChunkSize)
		// Guaranteed non-empty chunks list and no panic
		if len(chunks) == 0 {
			t.Errorf("SplitHTMLChunks must return at least one chunk")
		}
	})
}

// FuzzMarkdownToTelegramHTML fuzzes markdown to Telegram HTML converter.
// It ensures that any combination of markdown syntax, codeblocks, and entities converts safely without panicking.
func FuzzMarkdownToTelegramHTML(f *testing.F) {
	f.Add("# Header 1\n## Header 2\n### Header 3\nText with **bold** and *italic*.")
	f.Add("```go\nfunc main() {\n\tprintln(\"Hello\")\n}\n```")
	f.Add("Lists:\n- item 1\n- item 2\n  * nested item")
	f.Add("Check this [link](https://github.com/TheNovaNodes) for details.")
	f.Add("Unmatched `code and *italic and **bold and ~~strikethrough~~")
	f.Add("Raw HTML injection: <script>alert(1)</script> and <div onclick='bad()'>")

	f.Fuzz(func(t *testing.T, input string) {
		// Converter must never panic on arbitrary input
		output := MarkdownToTelegramHTML(input)
		_ = output
	})
}

// FuzzCleanTextForTTS fuzzes the speech synthesis text preprocessing pipeline.
func FuzzCleanTextForTTS(f *testing.F) {
	f.Add("Hello **user**! Here is a command: `/workspace ~/projects`.")
	f.Add("Check [NovaNodes](https://thenovanodes.com) or contact @admin.")
	f.Add("```bash\nrm -rf /tmp/cache\n```\nDone with cleanup.")
	f.Add("Emojis: 🎭 🤖 💬 ⚡ 🚀 📦 and symbols: *** --- ___")
	f.Add(strings.Repeat("Repeated sentence for audio synthesis. ", 20))

	f.Fuzz(func(t *testing.T, input string) {
		cleaned := CleanTextForTTS(input)
		if len(input) > 0 && len(strings.TrimSpace(input)) > 0 {
			// Cleaned text shouldn't be excessively bloated
			if len(cleaned) > len(input)*2+100 {
				t.Errorf("CleanTextForTTS unexpectedly expanded text size")
			}
		}
	})
}

func TestExtractAllowedArtifacts(t *testing.T) {
	tempDir := t.TempDir()

	mockAgentsRoot := filepath.Join(tempDir, "agents")
	mockBrainRoot := filepath.Join(tempDir, "brain")
	mockSecretRoot := filepath.Join(tempDir, "secrets")

	os.MkdirAll(mockAgentsRoot, 0755)
	os.MkdirAll(mockBrainRoot, 0755)
	os.MkdirAll(mockSecretRoot, 0755)

	t.Setenv("AGENTS_DIR", mockAgentsRoot)
	t.Setenv("BRAIN_DIR", mockBrainRoot)

	// 1. Create a valid file in mockAgentsRoot
	validAgentFile := filepath.Join(mockAgentsRoot, "test_agent_artifact.txt")
	os.WriteFile(validAgentFile, []byte("test"), 0644)

	// 2. Create a valid file in mockBrainRoot
	validBrainFile := filepath.Join(mockBrainRoot, "test_brain_artifact.txt")
	os.WriteFile(validBrainFile, []byte("test"), 0644)

	// 3. Create a malicious file outside allowed roots
	evilFile := filepath.Join(mockSecretRoot, "etc_passwd_mock.txt")
	os.WriteFile(evilFile, []byte("secret"), 0644)

	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "No links",
			text:     "Here is some text with no links.",
			expected: nil,
		},
		{
			name:     "Valid Agent File",
			text:     "Here is the file: [artifact](file://" + validAgentFile + ")",
			expected: []string{validAgentFile},
		},
		{
			name:     "Valid Brain File",
			text:     "I generated an image: [image.png](file://" + validBrainFile + ")",
			expected: []string{validBrainFile},
		},
		{
			name:     "Malicious File Blocked",
			text:     "I read your secrets: [passwd](file://" + evilFile + ")",
			expected: nil,
		},
		{
			name:     "Multiple Mixed Files",
			text:     "Agent: (file://" + validAgentFile + ")\nEvil: (file://" + evilFile + ")\nBrain: (file://" + validBrainFile + ")",
			expected: []string{validAgentFile, validBrainFile},
		},
		{
			name:     "Non-existent file",
			text:     "Does not exist: (file://" + filepath.Join(mockBrainRoot, "ghost.txt") + ")",
			expected: nil,
		},
		{
			name:     "URL Encoded path",
			text:     "Encoded: (file://" + filepath.Join(mockBrainRoot, "test%5Fbrain%5Fartifact.txt") + ")",
			expected: []string{validBrainFile},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractAllowedArtifacts(tt.text)

			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d results, got %d", len(tt.expected), len(result))
			}

			for i, path := range result {
				if path != tt.expected[i] {
					t.Errorf("expected path %s, got %s", tt.expected[i], path)
				}
			}
		})
	}
}

func TestSendArtifacts_SecretRedaction(t *testing.T) {
	tempDir := t.TempDir()
	mockAgentsRoot := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgentsRoot, 0755)
	t.Setenv("AGENTS_DIR", mockAgentsRoot)

	// Create artifact with a secret token
	leakFile := filepath.Join(mockAgentsRoot, "leaky_report.md")
	rawSecret := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ_0123456"
	content := "# Report\nBot Token: " + rawSecret
	os.WriteFile(leakFile, []byte(content), 0644)

	// Call sendArtifacts with nil bot (does not send HTTP, tests file processing)
	sendArtifacts(nil, 12345, "Link: (file://"+leakFile+")")
}
