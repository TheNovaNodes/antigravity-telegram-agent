package main

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
)

// utf16Len returns the number of UTF-16 code units in a string.
// Characters above 0xFFFF (e.g. 4-byte emoji runes and astral symbols) count as 2 units,
// exactly matching Telegram Bot API's message length metric.
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// sanitizeHref safely encodes URL ampersands as &amp; without double escaping.
func sanitizeHref(urlStr string) string {
	u := strings.ReplaceAll(urlStr, "&amp;", "&")
	u = strings.ReplaceAll(u, "&", "&amp;")
	u = strings.ReplaceAll(u, "\"", "&quot;")
	u = strings.ReplaceAll(u, "<", "&lt;")
	u = strings.ReplaceAll(u, ">", "&gt;")
	return u
}

// shieldFencedCodeBlocks extracts fenced code blocks according to CommonMark 4.5.
// Opening fence requires 3 or more backticks (or tildes).
// Closing fence requires matching character and count >= opening fence count.
// Code body is preserved verbatim, escaped via escapeHTML, and shielded into a placeholder.
func shieldFencedCodeBlocks(text string, createPlaceholder func(content string) string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	var outLines []string
	var codeLines []string

	inFence := false
	fenceChar := rune(0)
	fenceLen := 0
	fenceLang := ""

	for _, line := range lines {
		trimmedLeading := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmedLeading)

		if !inFence {
			// Check if line opens a fence (up to 3 spaces indentation allowed in CommonMark)
			if indent <= 3 && (strings.HasPrefix(trimmedLeading, "```") || strings.HasPrefix(trimmedLeading, "~~~")) {
				fc := rune(trimmedLeading[0])
				count := 0
				for _, r := range trimmedLeading {
					if r == fc {
						count++
					} else {
						break
					}
				}
				if count >= 3 {
					inFence = true
					fenceChar = fc
					fenceLen = count
					rest := strings.TrimSpace(trimmedLeading[count:])
					fields := strings.Fields(rest)
					if len(fields) > 0 {
						fenceLang = fields[0]
					} else {
						fenceLang = ""
					}
					codeLines = nil
					continue
				}
			}
			outLines = append(outLines, line)
		} else {
			// Inside fence: check for closing fence line
			if indent <= 3 && strings.HasPrefix(trimmedLeading, strings.Repeat(string(fenceChar), fenceLen)) {
				rest := strings.TrimSpace(trimmedLeading[fenceLen:])
				// Closing fence line may contain only fence characters and optional whitespace
				allFence := true
				for _, r := range rest {
					if r != fenceChar && r != ' ' && r != '\t' {
						allFence = false
						break
					}
				}
				if allFence {
					inFence = false
					codeBody := strings.Join(codeLines, "\n")
					if len(codeLines) > 0 {
						codeBody += "\n"
					}
					escapedBody := escapeHTML(codeBody)
					classAttr := ""
					if fenceLang != "" {
						classAttr = fmt.Sprintf(` class="language-%s"`, escapeHTML(fenceLang))
					}
					codeHTML := fmt.Sprintf("<pre><code%s>%s</code></pre>", classAttr, escapedBody)
					ph := createPlaceholder(codeHTML)
					outLines = append(outLines, ph)
					continue
				}
			}
			codeLines = append(codeLines, line)
		}
	}

	// Unclosed code block at EOF (CommonMark rule: closes at end of document)
	if inFence {
		codeBody := strings.Join(codeLines, "\n")
		if len(codeLines) > 0 {
			codeBody += "\n"
		}
		escapedBody := escapeHTML(codeBody)
		classAttr := ""
		if fenceLang != "" {
			classAttr = fmt.Sprintf(` class="language-%s"`, escapeHTML(fenceLang))
		}
		codeHTML := fmt.Sprintf("<pre><code%s>%s</code></pre>", classAttr, escapedBody)
		ph := createPlaceholder(codeHTML)
		outLines = append(outLines, ph)
	}

	return strings.Join(outLines, "\n")
}

// shieldInlineCodeSpans scans text for CommonMark 4.8 compliant code spans.
// It enforces:
// 1. Delimiter runs: opening K backticks must be closed by exact K backticks.
// 2. Paragraph reset: code spans cannot cross empty lines (\n\n). Unclosed backticks reset.
// 3. Stripping spaces: if content starts and ends with space (and is not all spaces), one space is removed.
// 4. Code content is HTML-escaped and wrapped in <code>...</code> and replaced with a placeholder.
func shieldInlineCodeSpans(text string, createPlaceholder func(content string) string) string {
	var b strings.Builder
	b.Grow(len(text))

	i := 0
	n := len(text)

	for i < n {
		if text[i] != '`' {
			b.WriteByte(text[i])
			i++
			continue
		}

		// Count opening delimiter run
		start := i
		for i < n && text[i] == '`' {
			i++
		}
		k := i - start

		found := false
		closeStart := -1
		j := i

		// Search forward for closing delimiter run of exact length k.
		// Stop if an empty line / paragraph break is encountered.
		for j < n {
			if text[j] == '\n' {
				kNL := j + 1
				for kNL < n && (text[kNL] == ' ' || text[kNL] == '\t' || text[kNL] == '\r') {
					kNL++
				}
				if kNL < n && text[kNL] == '\n' {
					// Paragraph break (\n\n) reached: unclosed code span resets!
					break
				}
			}

			if text[j] == '`' {
				runStart := j
				for j < n && text[j] == '`' {
					j++
				}
				runLen := j - runStart
				if runLen == k {
					found = true
					closeStart = runStart
					break
				}
				continue
			}
			j++
		}

		if found {
			rawContent := text[i:closeStart]
			// CommonMark strip rule:
			if len(rawContent) >= 2 && rawContent[0] == ' ' && rawContent[len(rawContent)-1] == ' ' {
				trimmed := strings.Trim(rawContent, " ")
				if trimmed != "" {
					rawContent = rawContent[1 : len(rawContent)-1]
				}
			}

			codeHTML := fmt.Sprintf("<code>%s</code>", escapeHTML(rawContent))
			ph := createPlaceholder(codeHTML)
			b.WriteString(ph)
			i = closeStart + k
		} else {
			// Reset: treat opening backticks as literal text in this paragraph
			b.WriteString(text[start:i])
		}
	}

	return b.String()
}

// shieldThinkingBlocks extracts strictly-closed LLM thinking blocks (<think>, <thinking>, <thought>).
// Unclosed tags remain in text and will be safely escaped by escapeHTML.
func shieldThinkingBlocks(text string, createPlaceholder func(content string) string) string {
	replaceBlock := func(m string, subs []string) string {
		body := strings.TrimSpace(subs[1])
		escaped := escapeHTML(body)
		formatted := fmt.Sprintf("<blockquote expandable>💭 <b>Thinking Process:</b>\n%s</blockquote>", escaped)
		return createPlaceholder(formatted)
	}

	text = reThink.ReplaceAllStringFunc(text, func(m string) string {
		return replaceBlock(m, reThink.FindStringSubmatch(m))
	})
	text = reThinking.ReplaceAllStringFunc(text, func(m string) string {
		return replaceBlock(m, reThinking.FindStringSubmatch(m))
	})
	text = reThought.ReplaceAllStringFunc(text, func(m string) string {
		return replaceBlock(m, reThought.FindStringSubmatch(m))
	})

	return text
}

// applyInlineFormatting transforms Markdown inline syntaxes into Telegram-compatible HTML tags.
func applyInlineFormatting(text string) string {
	// Images
	text = reTGImg.ReplaceAllStringFunc(text, func(m string) string {
		subs := reTGImg.FindStringSubmatch(m)
		alt := subs[1]
		url := sanitizeHref(subs[2])
		return fmt.Sprintf(`<a href="%s">🖼 %s</a>`, url, alt)
	})

	// Links
	text = reTGLink.ReplaceAllStringFunc(text, func(m string) string {
		subs := reTGLink.FindStringSubmatch(m)
		linkText := subs[1]
		url := sanitizeHref(subs[2])
		return fmt.Sprintf(`<a href="%s">%s</a>`, url, linkText)
	})

	// Spoilers
	text = reTGSpoiler.ReplaceAllString(text, `<tg-spoiler>$1</tg-spoiler>`)

	// Strikethrough
	text = reTGStrike.ReplaceAllString(text, `<s>$1</s>`)

	// Bold Italic
	text = reTGBoldItalic.ReplaceAllString(text, `<b><i>$1</i></b>`)

	// Bold
	text = reTGBold.ReplaceAllString(text, `<b>$1</b>`)

	// Italic (asterisk)
	text = reTGItalicAst.ReplaceAllString(text, `<i>$1</i>`)

	// Italic (underscore)
	text = reTGItalicUnd.ReplaceAllString(text, `<i>$1</i>`)

	return text
}

// parseMarkdownBlocks parses block-level elements (tables, blockquotes, headers, lists, HR)
// with strict DOM isolation so block elements cannot be corrupted by paragraph-level inline wrappers.
func parseMarkdownBlocks(text string, createPlaceholder func(content string) string) string {
	lines := strings.Split(text, "\n")
	var outLines []string
	var quoteBuffer []string
	isExpandableQuote := false
	var tableBuffer []string

	flushQuote := func() {
		if len(quoteBuffer) > 0 {
			qContent := strings.Join(quoteBuffer, "\n")
			formattedContent := applyInlineFormatting(qContent)
			attr := ""
			if isExpandableQuote {
				attr = " expandable"
			}
			quoteHTML := fmt.Sprintf("<blockquote%s>%s</blockquote>", attr, formattedContent)
			ph := createPlaceholder(quoteHTML)
			outLines = append(outLines, ph)
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

		// Table detection (exclude spoiler lines starting with '||')
		if strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|") && !strings.HasPrefix(stripped, "||") {
			flushQuote()
			tableBuffer = append(tableBuffer, stripped)
			continue
		} else if len(tableBuffer) > 0 {
			flushTable()
		}

		// Horizontal rule
		if stripped == "---" || stripped == "***" || stripped == "___" || stripped == "───────────────" {
			flushQuote()
			outLines = append(outLines, "───────────────")
			continue
		}

		// Blockquote
		if strings.HasPrefix(stripped, "&gt;") || strings.HasPrefix(stripped, ">") {
			var qLine string
			if strings.HasPrefix(stripped, "&gt; ") {
				qLine = stripped[5:]
			} else if strings.HasPrefix(stripped, "&gt;") {
				qLine = stripped[4:]
			} else if strings.HasPrefix(stripped, "> ") {
				qLine = stripped[2:]
			} else {
				qLine = stripped[1:]
			}

			if strings.HasPrefix(qLine, "[!NOTE]") || strings.HasPrefix(qLine, "[!IMPORTANT]") || strings.HasPrefix(qLine, "[!TIP]") {
				isExpandableQuote = true
			}
			quoteBuffer = append(quoteBuffer, qLine)
			continue
		} else if len(quoteBuffer) > 0 {
			flushQuote()
		}

		// Header
		if m := reTGHeader.FindStringSubmatch(stripped); m != nil {
			formattedHeader := applyInlineFormatting(m[2])
			outLines = append(outLines, fmt.Sprintf("\n<b><u>%s</u></b>", formattedHeader))
			continue
		}

		// Bullet list
		if m := reTGList.FindStringSubmatch(line); m != nil {
			formattedItem := applyInlineFormatting(m[2])
			outLines = append(outLines, fmt.Sprintf("%s• %s", m[1], formattedItem))
			continue
		}

		// Numbered list
		if m := reTGNumList.FindStringSubmatch(line); m != nil {
			formattedItem := applyInlineFormatting(m[3])
			outLines = append(outLines, fmt.Sprintf("%s%s. %s", m[1], m[2], formattedItem))
			continue
		}

		outLines = append(outLines, line)
	}

	flushQuote()
	flushTable()

	return strings.Join(outLines, "\n")
}

// shieldMarkdownCodeBlocksRaw isolates fenced code blocks and inline code spans,
// preserving their exact raw markdown representations inside placeholders.
// Used by rich_message.go to prevent thought tag regexes from corrupting markdown payloads.
func shieldMarkdownCodeBlocksRaw(text string) (string, map[string]string) {
	placeholders := make(map[string]string)
	createPlaceholder := func(content string) string {
		token := fmt.Sprintf("@@TGCODEPLACEHOLDER%s@@", strings.ReplaceAll(uuid.New().String(), "-", ""))
		placeholders[token] = content
		return token
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	var outLines []string
	var codeLines []string

	inFence := false
	fenceChar := rune(0)
	fenceLen := 0
	fenceHeader := ""

	for _, line := range lines {
		trimmedLeading := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmedLeading)

		if !inFence {
			if indent <= 3 && (strings.HasPrefix(trimmedLeading, "```") || strings.HasPrefix(trimmedLeading, "~~~")) {
				fc := rune(trimmedLeading[0])
				count := 0
				for _, r := range trimmedLeading {
					if r == fc {
						count++
					} else {
						break
					}
				}
				if count >= 3 {
					inFence = true
					fenceChar = fc
					fenceLen = count
					fenceHeader = line
					codeLines = nil
					continue
				}
			}
			outLines = append(outLines, line)
		} else {
			if indent <= 3 && strings.HasPrefix(trimmedLeading, strings.Repeat(string(fenceChar), fenceLen)) {
				rest := strings.TrimSpace(trimmedLeading[fenceLen:])
				allFence := true
				for _, r := range rest {
					if r != fenceChar && r != ' ' && r != '\t' {
						allFence = false
						break
					}
				}
				if allFence {
					inFence = false
					rawBlock := fenceHeader + "\n" + strings.Join(codeLines, "\n")
					if len(codeLines) > 0 {
						rawBlock += "\n"
					}
					rawBlock += line
					ph := createPlaceholder(rawBlock)
					outLines = append(outLines, ph)
					continue
				}
			}
			codeLines = append(codeLines, line)
		}
	}

	if inFence {
		rawBlock := fenceHeader + "\n" + strings.Join(codeLines, "\n")
		if len(codeLines) > 0 {
			rawBlock += "\n"
		}
		rawBlock += strings.Repeat(string(fenceChar), fenceLen)
		ph := createPlaceholder(rawBlock)
		outLines = append(outLines, ph)
	}

	text = strings.Join(outLines, "\n")

	// Shield inline code spans (CommonMark compliant with paragraph reset)
	var b strings.Builder
	b.Grow(len(text))

	i := 0
	n := len(text)
	for i < n {
		if text[i] != '`' {
			b.WriteByte(text[i])
			i++
			continue
		}

		start := i
		for i < n && text[i] == '`' {
			i++
		}
		k := i - start

		found := false
		closeStart := -1
		j := i

		for j < n {
			if text[j] == '\n' {
				kNL := j + 1
				for kNL < n && (text[kNL] == ' ' || text[kNL] == '\t' || text[kNL] == '\r') {
					kNL++
				}
				if kNL < n && text[kNL] == '\n' {
					break
				}
			}

			if text[j] == '`' {
				runStart := j
				for j < n && text[j] == '`' {
					j++
				}
				runLen := j - runStart
				if runLen == k {
					found = true
					closeStart = runStart
					break
				}
				continue
			}
			j++
		}

		if found {
			rawSpan := text[start : closeStart+k]
			ph := createPlaceholder(rawSpan)
			b.WriteString(ph)
			i = closeStart + k
		} else {
			b.WriteString(text[start:i])
		}
	}

	return b.String(), placeholders
}
