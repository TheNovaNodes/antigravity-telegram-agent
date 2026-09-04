package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/html"
)

var allowedTags = map[string]bool{
	"b": true, "strong": true,
	"i": true, "em": true,
	"u": true, "ins": true,
	"s": true, "strike": true, "del": true,
	"span": true, "tg-spoiler": true,
	"a":        true,
	"tg-emoji": true,
	"code":     true, "pre": true,
	"blockquote": true,
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

type openTagInfo struct {
	Tag        string
	Attributes string
}

// balanceAndSanitizeTelegramHTML ensures all tags are balanced and only allowed tags are used.
func balanceAndSanitizeTelegramHTML(rawHTML string) string {
	sanitized, _ := balanceAndSanitizeWithState(rawHTML, nil)
	return sanitized
}

// balanceAndSanitizeWithState balances tags, sanitizes input, and preserves open formatting tags across chunk boundaries.
func balanceAndSanitizeWithState(rawHTML string, initialOpenTags []openTagInfo) (string, []openTagInfo) {
	tokenizer := html.NewTokenizer(strings.NewReader(rawHTML))
	var out bytes.Buffer
	for _, ot := range initialOpenTags {
		out.WriteString("<" + ot.Tag + ot.Attributes + ">")
	}

	stack := make([]openTagInfo, len(initialOpenTags))
	copy(stack, initialOpenTags)

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}

		token := tokenizer.Token()

		switch tt {
		case html.TextToken:
			out.WriteString(escapeHTML(token.Data))
		case html.StartTagToken:
			tag := strings.ToLower(token.Data)
			if allowedTags[tag] {
				var attrBuf bytes.Buffer
				for _, attr := range token.Attr {
					key := strings.ToLower(attr.Key)
					if (tag == "a" && key == "href") ||
						(tag == "tg-emoji" && key == "emoji-id") ||
						(tag == "span" && key == "class") ||
						(tag == "pre" && key == "class") ||
						(tag == "code" && key == "class") ||
						(tag == "blockquote" && key == "expandable") {

						val := escapeHTML(attr.Val)
						if key == "expandable" {
							attrBuf.WriteString(` expandable`)
						} else {
							attrBuf.WriteString(fmt.Sprintf(` %s="%s"`, key, val))
						}
					}
				}
				attrStr := attrBuf.String()
				stack = append(stack, openTagInfo{Tag: tag, Attributes: attrStr})
				out.WriteString("<" + tag + attrStr + ">")
			} else {
				// Escape invalid tags so they appear as text
				out.WriteString("&lt;" + token.Data + "&gt;")
			}
		case html.EndTagToken:
			tag := strings.ToLower(token.Data)
			if allowedTags[tag] {
				// Find tag in stack
				idx := -1
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].Tag == tag {
						idx = i
						break
					}
				}
				if idx != -1 {
					// Pop and close everything down to idx
					for i := len(stack) - 1; i >= idx; i-- {
						out.WriteString("</" + stack[i].Tag + ">")
					}
					stack = stack[:idx]
				}
			} else {
				out.WriteString("&lt;/" + token.Data + "&gt;")
			}
		case html.SelfClosingTagToken:
			tag := strings.ToLower(token.Data)
			out.WriteString("&lt;" + tag + "/&gt;")
		}
	}

	unclosed := make([]openTagInfo, len(stack))
	copy(unclosed, stack)

	// Close remaining tags for this chunk to keep HTML valid
	for i := len(stack) - 1; i >= 0; i-- {
		out.WriteString("</" + stack[i].Tag + ">")
	}

	return out.String(), unclosed
}

// MarkdownToTelegramHTML converts standard Markdown into Telegram-compatible HTML.
// It handles bold, italic, code blocks, tables, and special Antigravity blocks like <think>.
func MarkdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	placeholders := make(map[string]string)
	createPlaceholder := func(content string) string {
		token := fmt.Sprintf("@@TGPLACEHOLDERSECUREUUID%s@@", strings.ReplaceAll(uuid.New().String(), "-", ""))
		placeholders[token] = content
		return token
	}

	// 1. Extract think blocks
	reThink := regexp.MustCompile(`(?is)<think>(.*?)(?:</think>|$)`)
	text = reThink.ReplaceAllStringFunc(text, func(m string) string {
		subs := reThink.FindStringSubmatch(m)
		body := strings.TrimSpace(subs[1])
		escaped := escapeHTML(body)
		formatted := fmt.Sprintf("<blockquote expandable>💭 <b>Thinking Process:</b>\n%s</blockquote>", escaped)
		return createPlaceholder(formatted)
	})

	reThinking := regexp.MustCompile(`(?is)<thinking>(.*?)(?:</thinking>|$)`)
	text = reThinking.ReplaceAllStringFunc(text, func(m string) string {
		subs := reThinking.FindStringSubmatch(m)
		body := strings.TrimSpace(subs[1])
		escaped := escapeHTML(body)
		formatted := fmt.Sprintf("<blockquote expandable>💭 <b>Thinking Process:</b>\n%s</blockquote>", escaped)
		return createPlaceholder(formatted)
	})

	// 2. Fenced Code Blocks
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.Count(text, "```")%2 != 0 {
		text += "\n```"
	}

	reFenced := regexp.MustCompile("(?is)```([a-zA-Z0-9_\\-\\+]*)\\n?(.*?)```")
	text = reFenced.ReplaceAllStringFunc(text, func(m string) string {
		subs := reFenced.FindStringSubmatch(m)
		lang := strings.TrimSpace(subs[1])
		body := escapeHTML(subs[2])
		classAttr := ""
		if lang != "" {
			classAttr = fmt.Sprintf(` class="language-%s"`, escapeHTML(lang))
		}
		return createPlaceholder(fmt.Sprintf("<pre><code%s>%s</code></pre>", classAttr, body))
	})

	// 3. Inline code
	reInline := regexp.MustCompile("(?s)`([^`]+?)`")
	text = reInline.ReplaceAllStringFunc(text, func(m string) string {
		subs := reInline.FindStringSubmatch(m)
		return createPlaceholder(fmt.Sprintf("<code>%s</code>", escapeHTML(subs[1])))
	})

	// Escape remaining HTML
	text = escapeHTML(text)

	// Block-level parsing
	lines := strings.Split(text, "\n")
	var outLines []string
	var quoteBuffer []string
	isExpandableQuote := false
	var tableBuffer []string

	flushQuote := func() {
		if len(quoteBuffer) > 0 {
			qContent := strings.Join(quoteBuffer, "\n")
			attr := ""
			if isExpandableQuote {
				attr = " expandable"
			}
			outLines = append(outLines, fmt.Sprintf("<blockquote%s>%s</blockquote>", attr, qContent))
			quoteBuffer = nil
			isExpandableQuote = false
		}
	}

	flushTable := func() {
		if len(tableBuffer) > 0 {
			tContent := strings.Join(tableBuffer, "\n")
			ph := createPlaceholder(fmt.Sprintf("<pre><code>%s</code></pre>", tContent))
			outLines = append(outLines, ph)
			tableBuffer = nil
		}
	}

	for _, line := range lines {
		stripped := strings.TrimSpace(line)

		// Table detection
		if strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|") {
			flushQuote()
			tableBuffer = append(tableBuffer, stripped)
			continue
		} else if len(tableBuffer) > 0 {
			flushTable()
		}

		if stripped == "---" || stripped == "***" || stripped == "___" || stripped == "───────────────" {
			flushQuote()
			outLines = append(outLines, "───────────────")
			continue
		}

		if strings.HasPrefix(stripped, "&gt; ") || strings.HasPrefix(stripped, "&gt;") {
			qLine := ""
			if strings.HasPrefix(stripped, "&gt; ") {
				qLine = stripped[5:]
			} else {
				qLine = stripped[4:]
			}

			if strings.HasPrefix(qLine, "[!NOTE]") || strings.HasPrefix(qLine, "[!IMPORTANT]") || strings.HasPrefix(qLine, "[!TIP]") {
				isExpandableQuote = true
			}
			quoteBuffer = append(quoteBuffer, qLine)
			continue
		} else if len(quoteBuffer) > 0 {
			flushQuote()
		}

		reHeader := regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
		if m := reHeader.FindStringSubmatch(stripped); m != nil {
			outLines = append(outLines, fmt.Sprintf("\n<b><u>%s</u></b>", m[2]))
			continue
		}

		reList := regexp.MustCompile(`^(\s*)[*\-+]\s+(.+)$`)
		if m := reList.FindStringSubmatch(line); m != nil {
			outLines = append(outLines, fmt.Sprintf("%s• %s", m[1], m[2]))
			continue
		}

		reNumList := regexp.MustCompile(`^(\s*)(\d+)\.\s+(.+)$`)
		if m := reNumList.FindStringSubmatch(line); m != nil {
			outLines = append(outLines, fmt.Sprintf("%s%s. %s", m[1], m[2], m[3]))
			continue
		}

		outLines = append(outLines, line)
	}

	flushQuote()
	flushTable()

	text = strings.Join(outLines, "\n")

	// Inline formatting
	// Images
	reImg := regexp.MustCompile(`!\[(.*?)\]\((https?://[^\s\)]+)\)`)
	text = reImg.ReplaceAllString(text, `<a href="$2">🖼 $1</a>`)

	// Links
	reLink := regexp.MustCompile(`\[(.*?)\]\((https?://[^\s\)]+)\)`)
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// Spoilers
	reSpoiler := regexp.MustCompile(`\|\|(.*?)\|\|`)
	text = reSpoiler.ReplaceAllString(text, `<tg-spoiler>$1</tg-spoiler>`)

	// Strikethrough
	reStrike := regexp.MustCompile(`~~(.*?)~~`)
	text = reStrike.ReplaceAllString(text, `<s>$1</s>`)

	// Bold Italic
	reBoldItalic := regexp.MustCompile(`\*\*\*(.*?)\*\*\*`)
	text = reBoldItalic.ReplaceAllString(text, `<b><i>$1</i></b>`)

	// Bold
	reBold := regexp.MustCompile(`\*\*(.*?)\*\*`)
	text = reBold.ReplaceAllString(text, `<b>$1</b>`)

	// Italic (asterisk)
	reItalicAst := regexp.MustCompile(`\*([^\s*](?:[^*]*[^\s*])?)\*`)
	text = reItalicAst.ReplaceAllString(text, `<i>$1</i>`)

	// Italic (underscore)
	reItalicUnd := regexp.MustCompile(`_([^\s_](?:[^_]*[^\s_])?)_`)
	text = reItalicUnd.ReplaceAllString(text, `<i>$1</i>`)

	// Restore placeholders
	for {
		replacedAny := false
		for token, original := range placeholders {
			if strings.Contains(text, token) {
				text = strings.ReplaceAll(text, token, original)
				replacedAny = true
			}
		}
		if !replacedAny {
			break
		}
	}

	return balanceAndSanitizeTelegramHTML(text)
}

// splitOversizedParagraph breaks a single oversized paragraph into chunks smaller than maxChunkSize,
// prioritizing newlines, spaces, and ensuring it never cuts in the middle of an HTML tag or entity.
func splitOversizedParagraph(p string, maxChunkSize int) []string {
	runes := []rune(p)
	if len(runes) <= maxChunkSize {
		return []string{p}
	}

	var parts []string
	for len(runes) > maxChunkSize {
		cut := maxChunkSize

		// Check if cutting inside an HTML tag <...>
		inTag := false
		tagStart := -1
		for i := cut - 1; i >= 0; i-- {
			if runes[i] == '>' {
				break
			}
			if runes[i] == '<' {
				inTag = true
				tagStart = i
				break
			}
		}

		// Also check if cutting inside an HTML entity &...;
		inEntity := false
		entityStart := -1
		if !inTag {
			for i := cut - 1; i >= 0 && (cut-i) < 12; i-- {
				if runes[i] == ';' || runes[i] == ' ' || runes[i] == '\n' {
					break
				}
				if runes[i] == '&' {
					inEntity = true
					entityStart = i
					break
				}
			}
		}

		if inTag && tagStart > 0 {
			cut = tagStart
		} else if inEntity && entityStart > 0 {
			cut = entityStart
		} else if !inTag && !inEntity {
			// Try to find the nearest newline or space in the last 20% of the chunk to avoid word-severing
			minCut := cut * 4 / 5
			for i := cut - 1; i >= minCut; i-- {
				if runes[i] == '\n' || runes[i] == ' ' {
					cut = i + 1
					break
				}
			}
		}

		if cut <= 0 {
			// Fallback: find closing '>' if tag started at 0
			closingTag := -1
			for i := 0; i < len(runes); i++ {
				if runes[i] == '>' {
					closingTag = i + 1
					break
				}
			}
			if closingTag > 0 && closingTag <= len(runes) {
				cut = closingTag
			} else {
				cut = maxChunkSize
			}
		}

		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}

	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// SplitHTMLChunks breaks a long HTML string into an array of smaller chunks
// that comply with Telegram's message length limits, ensuring HTML tags are balanced
// and cross-chunk open formatting tags are preserved without breaking mid-tag or mid-entity.
func SplitHTMLChunks(text string, maxChunkSize int) []string {
	if len(text) <= maxChunkSize {
		return []string{balanceAndSanitizeTelegramHTML(text)}
	}

	paragraphs := strings.Split(text, "\n\n")
	var rawChunks []string
	var currentChunk []string
	currentLength := 0

	for _, p := range paragraphs {
		if currentLength+len(p)+2 > maxChunkSize {
			if len(currentChunk) > 0 {
				rawChunks = append(rawChunks, strings.Join(currentChunk, "\n\n"))
				currentChunk = nil
				currentLength = 0
			}

			// If a single paragraph is too large, split it safely respecting HTML tags & entities
			parts := splitOversizedParagraph(p, maxChunkSize)
			for i, part := range parts {
				if i < len(parts)-1 {
					rawChunks = append(rawChunks, part)
				} else {
					if len(part) > 0 {
						currentChunk = append(currentChunk, part)
						currentLength = len(part)
					}
				}
			}
		} else {
			currentChunk = append(currentChunk, p)
			currentLength += len(p) + 2
		}
	}

	if len(currentChunk) > 0 {
		rawChunks = append(rawChunks, strings.Join(currentChunk, "\n\n"))
	}

	var chunks []string
	var openTags []openTagInfo
	for _, raw := range rawChunks {
		var chunkHTML string
		chunkHTML, openTags = balanceAndSanitizeWithState(raw, openTags)
		if strings.TrimSpace(chunkHTML) != "" {
			chunks = append(chunks, chunkHTML)
		}
	}

	if len(chunks) == 0 {
		return []string{""}
	}

	return chunks
}
