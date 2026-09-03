package main

import (
	"strings"
	"testing"
)

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
