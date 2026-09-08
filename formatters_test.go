package main

import (
	"strings"
	"testing"
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
