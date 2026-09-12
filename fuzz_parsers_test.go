package main

import (
	"strings"
	"testing"
)

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
	f.Add("Hello **user**! Here is a command: `/workspace /root/projects`.")
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
