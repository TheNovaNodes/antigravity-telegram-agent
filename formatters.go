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

var (
	reThink    = regexp.MustCompile(`(?is)<think>(.*?)</think>`)
	reThinking = regexp.MustCompile(`(?is)<thinking>(.*?)</thinking>`)
	reThought  = regexp.MustCompile(`(?is)<thought>(.*?)</thought>`)

	reTGHeader  = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	reTGList    = regexp.MustCompile(`^(\s*)[*\-+]\s+(.+)$`)
	reTGNumList = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.+)$`)

	reTGImg        = regexp.MustCompile(`!\[(.*?)\]\((https?://[^\s\)]+)\)`)
	reTGLink       = regexp.MustCompile(`\[(.*?)\]\((https?://[^\s\)]+)\)`)
	reTGSpoiler    = regexp.MustCompile(`\|\|(.*?)\|\|`)
	reTGStrike     = regexp.MustCompile(`~~(.*?)~~`)
	reTGBoldItalic = regexp.MustCompile(`\*\*\*(.*?)\*\*\*`)
	reTGBold       = regexp.MustCompile(`\*\*(.*?)\*\*`)
	reTGItalicAst  = regexp.MustCompile(`\*([^\s*](?:[^*]*[^\s*])?)\*`)
	reTGItalicUnd  = regexp.MustCompile(`_([^\s_](?:[^_]*[^\s_])?)_`)
)

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// escapeMarkdown escapes special formatting characters for Telegram legacy Markdown parse mode.
func escapeMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"_", "\\_",
		"*", "\\*",
		"`", "\\`",
		"[", "\\[",
	)
	return replacer.Replace(text)
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

	// 0. Code Shielding First: Fenced Code Blocks (CommonMark 4.5) & Inline Code Spans (CommonMark 4.8)
	text = shieldFencedCodeBlocks(text, createPlaceholder)
	text = shieldInlineCodeSpans(text, createPlaceholder)

	// 1. Extract strictly-paired thinking blocks (OpSec & Stream Protection)
	text = shieldThinkingBlocks(text, createPlaceholder)

	// 2. Escape any remaining raw HTML entities and tags
	text = escapeHTML(text)

	// 3. Block-Level Lexing & Parsing with Strict DOM Isolation
	text = parseMarkdownBlocks(text, createPlaceholder)

	// 4. Inline formatting (links, spoilers, strikethrough, bold, italic)
	text = applyInlineFormatting(text)

	// 5. Restore placeholders in reverse or until none remain
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
// prioritizing newlines, spaces, and ensuring it never cuts in the middle of an HTML tag, entity,
// or UTF-16 surrogate pair.
func splitOversizedParagraph(p string, maxChunkSize int) []string {
	if utf16Len(p) <= maxChunkSize {
		return []string{p}
	}

	runes := []rune(p)
	var parts []string

	for len(runes) > 0 && utf16Len(string(runes)) > maxChunkSize {
		// Find maximum rune cut such that utf16Len(runes[:cut]) <= maxChunkSize
		cut := 0
		currentUnits := 0
		for i, r := range runes {
			u := 1
			if r > 0xFFFF {
				u = 2
			}
			if currentUnits+u > maxChunkSize {
				cut = i
				break
			}
			currentUnits += u
			cut = i + 1
		}

		if cut == 0 {
			cut = 1
		}

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
				cut = 1
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
// Length is measured in UTF-16 code units (len(utf16.Encode([]rune(chunk)))) to guarantee
// compliance with Telegram Bot API limits when strings contain 4-byte emojis or surrogate pairs.
func SplitHTMLChunks(text string, maxChunkSize int) []string {
	if utf16Len(text) <= maxChunkSize {
		return []string{balanceAndSanitizeTelegramHTML(text)}
	}

	paragraphs := strings.Split(text, "\n\n")
	var rawChunks []string
	var currentChunk []string
	currentLength := 0

	for _, p := range paragraphs {
		pLen := utf16Len(p)
		if currentLength+pLen+2 > maxChunkSize {
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
						currentLength = utf16Len(part)
					}
				}
			}
		} else {
			currentChunk = append(currentChunk, p)
			currentLength += pLen + 2
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
