package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// RichMessageThreshold is the character threshold (in runes) above which structured responses switch to sendRichMessage.
	RichMessageThreshold = 1500

	// ClassicMessageLimit is the maximum character limit for classic Telegram sendMessage (HTML/Markdown).
	ClassicMessageLimit = 4000

	// MaxRichMessageLength is the hard limit for Telegram sendRichMessage format (32,768 UTF-8 characters).
	MaxRichMessageLength = 32768

	// PreviewTruncateLimit is the character target for Tier 3 (>32KB) preview summaries.
	PreviewTruncateLimit = 2500
)

// DeliveryTier represents the routing strategy for outbound agent responses.
type DeliveryTier int

const (
	// Tier1ClassicBubble: < 1500 runes or plain text without Markdown structure -> classic sendMessage (HTML).
	Tier1ClassicBubble DeliveryTier = 1

	// Tier2RichArticle: >= 1500 runes with Markdown structure, >= 3000 runes, or contains tables/thoughts -> monolithic sendRichMessage.
	Tier2RichArticle DeliveryTier = 2

	// Tier3MarkdownArtifact: > 32768 runes -> preview summary via sendRichMessage + .md Telegram document.
	Tier3MarkdownArtifact DeliveryTier = 3
)

// RichBlockThinking represents a collapsible thinking / Chain-of-Thought block for LLM models.
type RichBlockThinking struct {
	Text      string `json:"text"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// InputRichMessage represents the payload for Telegram Bot API's sendRichMessage method.
type InputRichMessage struct {
	ChatID      int64              `json:"chat_id,omitempty"`
	Markdown    string             `json:"markdown"`
	Thinking    *RichBlockThinking `json:"thinking,omitempty"`
	ReplyMarkup any                `json:"reply_markup,omitempty"`
}

// Regex to capture LLM thought/thinking blocks like <thought>...</thought> or <think>...</think>.
// Strict closing tags are required without greedy trailing capture ($).
var (
	thoughtRegex = regexp.MustCompile(`(?is)<thought>(.*?)</thought>|<think>(.*?)</think>|<thinking>(.*?)</thinking>`)
)

// shieldMarkdownCode replaces fenced and inline code blocks with unique tokens
// so thinking tag parsers ignore any tags inside code blocks.
func shieldMarkdownCode(text string) (string, map[string]string) {
	return shieldMarkdownCodeBlocksRaw(text)
}

// restoreMarkdownCode restores previously shielded code blocks in markdown text.
func restoreMarkdownCode(text string, placeholders map[string]string) string {
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
	return text
}

// SanitizeRichMessageText enforces a strict UTF-8 rune limit of 32,768 characters.
func SanitizeRichMessageText(text string) string {
	runes := []rune(text)
	if len(runes) > MaxRichMessageLength {
		truncNotice := "\n\n⚠️ _[Truncated to 32KB limit]_"
		noticeRunes := []rune(truncNotice)
		avail := MaxRichMessageLength - len(noticeRunes)
		if avail > 0 && avail < len(runes) {
			return string(runes[:avail]) + truncNotice
		}
		return string(runes[:MaxRichMessageLength])
	}
	return text
}

// isTableSeparatorLine determines if a line is a Markdown table separator like |---|---| or |:---:|---:|.
func isTableSeparatorLine(line string) bool {
	trimmed := strings.Trim(line, "| \t")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r != '-' && r != ':' && r != '|' && r != ' ' && r != '\t' {
			return false
		}
	}
	return strings.Contains(trimmed, "-")
}

// HasMarkdownTable checks if the provided text contains a valid Markdown table structure.
func HasMarkdownTable(text string) bool {
	lines := strings.Split(text, "\n")
	hasHeader := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") && strings.Count(trimmed, "|") >= 2 {
			if isTableSeparatorLine(trimmed) {
				if hasHeader {
					return true
				}
			} else {
				hasHeader = true
			}
		} else {
			hasHeader = false
		}
	}
	return false
}

// HasThoughts checks if the text contains LLM thought/thinking blocks outside code blocks.
func HasThoughts(text string) bool {
	if !strings.Contains(text, "<") {
		return false
	}
	if !strings.Contains(text, "`") {
		return thoughtRegex.MatchString(text)
	}
	shielded, _ := shieldMarkdownCode(text)
	return thoughtRegex.MatchString(shielded)
}

// isMarkdownHeading reports whether a trimmed line represents a Markdown heading (^#{1,6}\s).
func isMarkdownHeading(s string) bool {
	count := 0
	for count < len(s) && s[count] == '#' {
		count++
	}
	if count >= 1 && count <= 6 && count < len(s) && (s[count] == ' ' || s[count] == '\t') {
		return true
	}
	return false
}

// isCodeFence reports whether a trimmed line is a fenced code block boundary (``` or ~~~).
func isCodeFence(s string) bool {
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")
}

// isBlockquote reports whether a trimmed line is a blockquote (^>\s or ^>$).
func isBlockquote(s string) bool {
	if s == ">" {
		return true
	}
	return strings.HasPrefix(s, "> ") || strings.HasPrefix(s, ">\t")
}

// isBulletListItem reports whether a trimmed line is a bullet list item (- , * , + , • ).
func isBulletListItem(s string) bool {
	return strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "-\t") ||
		strings.HasPrefix(s, "* ") || strings.HasPrefix(s, "*\t") ||
		strings.HasPrefix(s, "+ ") || strings.HasPrefix(s, "+\t") ||
		strings.HasPrefix(s, "• ") || strings.HasPrefix(s, "•\t")
}

// isNumberedListItem reports whether a trimmed line is a numbered list item (^[0-9]{1,9}(\.|\))\s).
func isNumberedListItem(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i < 1 || i > 9 || i >= len(s) {
		return false
	}
	if s[i] != '.' && s[i] != ')' {
		return false
	}
	i++
	if i >= len(s) || (s[i] != ' ' && s[i] != '\t') {
		return false
	}
	return true
}

// isHorizontalRule reports whether a trimmed line is a horizontal rule (---, ***, ___ >= 3 chars, allowing whitespace).
func isHorizontalRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	ch := s[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ch {
			count++
		} else if c != ' ' && c != '\t' {
			return false
		}
	}
	return count >= 3
}

// isBoldHeaderPlate reports whether a trimmed line is a bold header plate (^(\*\*|__)[^\n]+(\*\*|__)).
func isBoldHeaderPlate(s string) bool {
	var marker string
	if strings.HasPrefix(s, "**") {
		marker = "**"
	} else if strings.HasPrefix(s, "__") {
		marker = "__"
	} else {
		return false
	}

	rest := s[2:]
	idx := strings.Index(rest, marker)
	return idx > 0
}

// HasMarkdownStructure performs a zero-allocation line-by-line inspection of text
// to detect standard Markdown structural elements:
// - Headings: ^#{1,6}\s
// - Code blocks: ``` or ~~~
// - Blockquotes: ^>\s or ^>$
// - Bullet lists: - , * , + , •
// - Numbered lists: 1. , 1)
// - Horizontal rules: ---, ***, ___ (>= 3 chars)
// - Bold header plates: ^(**|__)[^\n]+(**|__)
// - Tables: HasMarkdownTable
func HasMarkdownStructure(text string) bool {
	if text == "" {
		return false
	}
	if strings.Contains(text, "|") && HasMarkdownTable(text) {
		return true
	}
	remaining := text
	for len(remaining) > 0 {
		var line string
		idx := strings.IndexByte(remaining, '\n')
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = ""
		}

		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if isMarkdownHeading(trimmed) ||
			isCodeFence(trimmed) ||
			isBlockquote(trimmed) ||
			isBulletListItem(trimmed) ||
			isNumberedListItem(trimmed) ||
			isHorizontalRule(trimmed) ||
			isBoldHeaderPlate(trimmed) {
			return true
		}
	}
	return false
}

// DetermineDeliveryTier evaluates which tier should be used to deliver the response:
// Tier 1 (Classic Bubble): Short (< 1500 runes) flat conversational replies without Markdown structure.
// Tier 2 (Rich Article): 1500..32768 runes with Markdown structure, >= 3000 runes, or containing tables/thoughts/large HTML.
// Tier 3 (Markdown Artifact): > 32768 runes -> preview summary + .md file artifact.
func DetermineDeliveryTier(text string) DeliveryTier {
	runeLen := utf8.RuneCountInString(text)
	if runeLen > MaxRichMessageLength {
		return Tier3MarkdownArtifact
	}
	if HasMarkdownTable(text) || HasThoughts(text) || utf8.RuneCountInString(MarkdownToTelegramHTML(text)) > ClassicMessageLimit {
		return Tier2RichArticle
	}
	if runeLen >= RichMessageThreshold && HasMarkdownStructure(text) {
		return Tier2RichArticle
	}
	if runeLen >= 3000 {
		return Tier2RichArticle
	}
	return Tier1ClassicBubble
}

// ShouldUseRichMessage evaluates whether a message warrants routing to sendRichMessage.
// Returns true for Tier2RichArticle (>= 1500 runes with Markdown structure, >= 3000 runes, tables/thoughts/large HTML, up to 32768 runes).
func ShouldUseRichMessage(text string) bool {
	return DetermineDeliveryTier(text) == Tier2RichArticle
}

// TruncateMarkdownSafely truncates text to approximately maxRunes while preserving
// Markdown integrity (safely closing open fenced code blocks and avoiding mid-token splits).
func TruncateMarkdownSafely(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}

	cutIdx := maxRunes
	foundCut := false

	// Proportional lookback windows so we don't discard excessive content when maxRunes is small
	lookbackParagraph := 400
	if lookbackParagraph > maxRunes/4 {
		lookbackParagraph = maxRunes / 4
	}
	lookbackNewline := 200
	if lookbackNewline > maxRunes/5 {
		lookbackNewline = maxRunes / 5
	}
	lookbackSpace := 80
	if lookbackSpace > maxRunes/6 {
		lookbackSpace = maxRunes / 6
	}

	// 1. Try paragraph break (\n\n) within lookbackParagraph runes
	if lookbackParagraph > 0 {
		limitPara := cutIdx - lookbackParagraph
		for i := cutIdx - 1; i > limitPara; i-- {
			if runes[i-1] == '\n' && runes[i] == '\n' {
				cutIdx = i - 1
				foundCut = true
				break
			}
		}
	}

	// 2. Try single newline (\n) within lookbackNewline runes
	if !foundCut && lookbackNewline > 0 {
		limitNL := cutIdx - lookbackNewline
		for i := cutIdx - 1; i >= limitNL; i-- {
			if runes[i] == '\n' {
				cutIdx = i
				foundCut = true
				break
			}
		}
	}

	// 3. Try space (' ') within lookbackSpace runes
	if !foundCut && lookbackSpace > 0 {
		limitSpace := cutIdx - lookbackSpace
		for i := cutIdx - 1; i >= limitSpace; i-- {
			if runes[i] == ' ' {
				cutIdx = i
				foundCut = true
				break
			}
		}
	}

	truncated := strings.TrimRight(string(runes[:cutIdx]), " \t\r\n")

	// Scan lines to detect if an open fenced code block is left unclosed
	lines := strings.Split(truncated, "\n")
	inCodeBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
		}
	}

	if inCodeBlock {
		truncated += "\n```"
	}

	return truncated
}

func getArtifactSaveDir(targetDirs ...string) string {
	for _, dir := range targetDirs {
		if dir != "" {
			agentDir := filepath.Join(dir, "scratch", "downloads")
			if err := os.MkdirAll(agentDir, 0700); err == nil {
				return agentDir
			}
		}
	}

	// 1. Try local scratch/downloads if it exists in current working dir
	localDir := filepath.Join("scratch", "downloads")
	if info, err := os.Stat(localDir); err == nil && info.IsDir() {
		return localDir
	}

	// 2. Try common agents scratch/downloads (shared across all bot agents, zero bot name hardcoding)
	agentsBase := getAgentsDir()
	commonDir := filepath.Join(agentsBase, "common", "scratch", "downloads")
	if err := os.MkdirAll(commonDir, 0700); err == nil {
		return commonDir
	}

	// 3. Fallback to os.TempDir() / antigravity-bot / scratch / downloads
	tmpDir := filepath.Join(os.TempDir(), "antigravity-bot", "scratch", "downloads")
	_ = os.MkdirAll(tmpDir, 0700)
	return tmpDir
}

// RotateArtifactFiles prunes older response_*.md artifact files in dir,
// keeping at most maxKeep newest files and pruning any files older than maxAge.
func RotateArtifactFiles(dir string, maxKeep int, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	type fileMeta struct {
		name    string
		modTime time.Time
	}
	var artifacts []fileMeta
	now := time.Now()

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "response_") && strings.HasSuffix(name, ".md") {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			artifacts = append(artifacts, fileMeta{
				name:    name,
				modTime: info.ModTime(),
			})
		}
	}

	// Sort newest first
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].modTime.After(artifacts[j].modTime)
	})

	removed := 0
	for i, f := range artifacts {
		shouldRemove := false
		if maxAge > 0 && now.Sub(f.modTime) > maxAge {
			shouldRemove = true
		} else if maxKeep > 0 && i >= maxKeep {
			shouldRemove = true
		}

		if shouldRemove {
			fullPath := filepath.Join(dir, f.name)
			if err := os.Remove(fullPath); err == nil {
				removed++
			}
		}
	}

	return removed
}

// ExtractThinkingAndMarkdown extracts thinking / CoT blocks from raw model output,
// returning clean markdown and an optional RichBlockThinking struct.
// Any thought tags inside code blocks (fenced or inline) are preserved in markdown.
func ExtractThinkingAndMarkdown(text string) (string, *RichBlockThinking) {
	shielded := text
	var placeholders map[string]string
	if strings.Contains(text, "`") {
		shielded, placeholders = shieldMarkdownCode(text)
	}

	var thoughts []string
	cleanText := thoughtRegex.ReplaceAllStringFunc(shielded, func(m string) string {
		subs := thoughtRegex.FindStringSubmatch(m)
		for i := 1; i < len(subs); i++ {
			if subs[i] != "" {
				t := strings.TrimSpace(subs[i])
				if t != "" {
					thoughts = append(thoughts, t)
				}
				break
			}
		}
		return ""
	})

	if len(placeholders) > 0 {
		cleanText = restoreMarkdownCode(cleanText, placeholders)
	}

	var thinking *RichBlockThinking
	if len(thoughts) > 0 {
		thinkingText := strings.TrimSpace(strings.Join(thoughts, "\n\n"))
		thinking = &RichBlockThinking{
			Text:      SanitizeRichMessageText(thinkingText),
			Collapsed: true,
		}
	}

	markdown := strings.TrimSpace(cleanText)
	if markdown == "" && thinking != nil {
		// If text had ONLY thoughts, ensure markdown is not completely empty
		markdown = thinking.Text
		thinking = nil
	}

	return markdown, thinking
}

// BuildInputRichMessage extracts thinking blocks (if any), applies UTF-8 length sanitization,
// and constructs an InputRichMessage ready for serialization.
func BuildInputRichMessage(text string) InputRichMessage {
	cleanMarkdown, thinking := ExtractThinkingAndMarkdown(text)
	cleanMarkdown = SanitizeRichMessageText(cleanMarkdown)
	return InputRichMessage{
		Markdown: cleanMarkdown,
		Thinking: thinking,
	}
}

// prepareRichMessagePayload serializes and sanitizes InputRichMessage into a JSON payload.
func prepareRichMessagePayload(input any) (string, error) {
	var richMsg InputRichMessage
	switch v := input.(type) {
	case InputRichMessage:
		richMsg = v
	case *InputRichMessage:
		if v != nil {
			richMsg = *v
		}
	case string:
		richMsg = BuildInputRichMessage(v)
	default:
		return "", fmt.Errorf("unsupported input type for rich_message: %T", input)
	}

	richMsg.Markdown = SanitizeRichMessageText(richMsg.Markdown)
	if strings.TrimSpace(richMsg.Markdown) == "" {
		if richMsg.Thinking != nil && strings.TrimSpace(richMsg.Thinking.Text) != "" {
			richMsg.Markdown = richMsg.Thinking.Text
			richMsg.Thinking = nil
		} else {
			richMsg.Markdown = "(empty message)"
		}
	}

	richPayload, err := json.Marshal(richMsg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal rich_message: %w", err)
	}
	return string(richPayload), nil
}

// sendRichMessage makes a request to the Telegram Bot API sendRichMessage endpoint.
func sendRichMessage(bot *tgbotapi.BotAPI, chatID int64, input any, markups ...*tgbotapi.InlineKeyboardMarkup) (*tgbotapi.Message, error) {
	if bot == nil {
		return nil, fmt.Errorf("bot instance is nil")
	}

	richPayload, err := prepareRichMessagePayload(input)
	if err != nil {
		return nil, err
	}

	params := make(tgbotapi.Params)
	params["chat_id"] = strconv.FormatInt(chatID, 10)
	params["rich_message"] = richPayload

	if len(markups) > 0 && markups[0] != nil {
		markupData, err := json.Marshal(markups[0])
		if err == nil {
			params["reply_markup"] = string(markupData)
		}
	}

	resp, err := bot.MakeRequest("sendRichMessage", params)
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, fmt.Errorf("telegram API error: %s (code %d)", resp.Description, resp.ErrorCode)
	}

	var sentMsg tgbotapi.Message
	if len(resp.Result) > 0 {
		_ = json.Unmarshal(resp.Result, &sentMsg)
	}
	return &sentMsg, nil
}

// editRichMessage edits an existing message in-place using the Telegram Bot API editMessageText method with rich_message payload.
func editRichMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, input any, markups ...*tgbotapi.InlineKeyboardMarkup) (*tgbotapi.Message, error) {
	if bot == nil {
		return nil, fmt.Errorf("bot instance is nil")
	}

	richPayload, err := prepareRichMessagePayload(input)
	if err != nil {
		return nil, err
	}

	params := make(tgbotapi.Params)
	params["chat_id"] = strconv.FormatInt(chatID, 10)
	params["message_id"] = strconv.Itoa(messageID)
	params["rich_message"] = richPayload

	if len(markups) > 0 && markups[0] != nil {
		markupData, err := json.Marshal(markups[0])
		if err == nil {
			params["reply_markup"] = string(markupData)
		}
	}

	resp, err := bot.MakeRequest("editMessageText", params)
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, fmt.Errorf("telegram API error: %s (code %d)", resp.Description, resp.ErrorCode)
	}

	var editedMsg tgbotapi.Message
	if len(resp.Result) > 0 {
		_ = json.Unmarshal(resp.Result, &editedMsg)
	}
	return &editedMsg, nil
}

// sendAdaptiveResponse routes outbound responses across the Tri-Modal Delivery Architecture:
// Tier 1 (Classic Bubble): < 1500 runes or plain text without Markdown structure -> classic sendMessage (HTML).
// Tier 2 (Rich Article): >= 1500 runes with Markdown structure, >= 3000 runes, or contains tables/thoughts/large HTML -> monolithic sendRichMessage / editRichMessage.
// Tier 3 (Markdown Artifact): > 32768 runes -> preview summary via sendRichMessage + .md Telegram document.
func sendAdaptiveResponse(bot *tgbotapi.BotAPI, chatID int64, activeMsgID int, text string, markups ...*tgbotapi.InlineKeyboardMarkup) []string {
	return sendAdaptiveResponseWithWorkspace(bot, chatID, activeMsgID, text, "", markups...)
}

// sendAdaptiveResponseWithWorkspace routes outbound responses across the Tri-Modal Delivery Architecture
// while respecting the session workspace for isolated artifact storage.
func sendAdaptiveResponseWithWorkspace(bot *tgbotapi.BotAPI, chatID int64, activeMsgID int, text string, workspace string, markups ...*tgbotapi.InlineKeyboardMarkup) []string {
	tier := DetermineDeliveryTier(text)

	if bot == nil {
		formatted := MarkdownToTelegramHTML(text)
		if strings.TrimSpace(formatted) == "" {
			if strings.TrimSpace(text) != "" {
				formatted = escapeHTML(text)
			} else {
				formatted = "<i>(empty message)</i>"
			}
		}
		chunks := SplitHTMLChunks(formatted, ClassicMessageLimit)
		if len(chunks) == 0 {
			chunks = []string{"<i>(empty message)</i>"}
		}
		return chunks
	}

	switch tier {
	case Tier3MarkdownArtifact:
		totalRunes := utf8.RuneCountInString(text)
		previewText := TruncateMarkdownSafely(text, PreviewTruncateLimit)
		notice := fmt.Sprintf("\n\n---\n📄 **Полный ответ (%d знаков) сформирован и прикреплён файлом-артефактом ниже.**", totalRunes)
		fullPreview := previewText + notice

		// 1. Send Preview via sendRichMessage (or fallback to classic HTML)
		_, err := sendRichMessage(bot, chatID, fullPreview, markups...)
		if err != nil {
			log.Printf("Tier 3 sendRichMessage preview failed for chatID %d (len %d): %v, falling back to classic preview", chatID, len(text), err)
			formattedPreview := MarkdownToTelegramHTML(fullPreview)
			msg := tgbotapi.NewMessage(chatID, formattedPreview)
			msg.ParseMode = "HTML"
			if len(markups) > 0 && markups[0] != nil {
				msg.ReplyMarkup = markups[0]
			}
			bot.Send(msg)
		}

		// 2. Eliminate streaming draft atomically
		if activeMsgID != 0 {
			delMsg := tgbotapi.NewDeleteMessage(chatID, activeMsgID)
			_, _ = bot.Send(delMsg)
		}

		// 3. Save full unclipped markdown to scratch/downloads/response_<timestamp>.md (0600)
		artifactDir := getArtifactSaveDir(workspace)
		RotateArtifactFiles(artifactDir, 20, 24*time.Hour)

		timestamp := time.Now().UTC().Format("20060102_150405")
		fileName := fmt.Sprintf("response_%s.md", timestamp)
		filePath := filepath.Join(artifactDir, fileName)

		if writeErr := os.WriteFile(filePath, []byte(text), 0600); writeErr != nil {
			log.Printf("[Artifacts] Failed to save Tier 3 artifact %s: %v", filePath, writeErr)
		}

		// 4. Send as Telegram Document with readable name "agent_response.md"
		if f, openErr := os.Open(filePath); openErr == nil {
			defer f.Close()
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
				Name:   "agent_response.md",
				Reader: f,
			})
			doc.Caption = fmt.Sprintf("📄 agent_response.md (%d знаков)", totalRunes)
			if _, docErr := bot.Send(doc); docErr != nil {
				log.Printf("[Artifacts] Failed to send Tier 3 document to chat %d: %v", chatID, docErr)
			}
		} else {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
				Name:   "agent_response.md",
				Reader: bytes.NewReader([]byte(text)),
			})
			doc.Caption = fmt.Sprintf("📄 agent_response.md (%d знаков)", totalRunes)
			if _, docErr := bot.Send(doc); docErr != nil {
				log.Printf("[Artifacts] Failed to send Tier 3 in-memory document to chat %d: %v", chatID, docErr)
			}
		}

		return []string{fullPreview}

	case Tier2RichArticle:
		if activeMsgID != 0 {
			_, err := editRichMessage(bot, chatID, activeMsgID, text, markups...)
			if err == nil {
				return []string{text}
			}
			log.Printf("editRichMessage failed for chatID %d msgID %d (len %d), falling back to classic cascade: %v", chatID, activeMsgID, len(text), err)
		} else {
			_, err := sendRichMessage(bot, chatID, text, markups...)
			if err == nil {
				return []string{text}
			}
			log.Printf("sendRichMessage failed for chatID %d (len %d), falling back to classic cascade: %v", chatID, len(text), err)
		}

	case Tier1ClassicBubble:
		// Fall through to classic bubble delivery
	}

	// Classic delivery (Tier 1 or Tier 2 fallback)
	if activeMsgID == 0 {
		formatted := MarkdownToTelegramHTML(text)
		if strings.TrimSpace(formatted) == "" {
			if strings.TrimSpace(text) != "" {
				formatted = escapeHTML(text)
			} else {
				formatted = "<i>(empty message)</i>"
			}
		}
		chunks := SplitHTMLChunks(formatted, ClassicMessageLimit)
		if len(chunks) == 0 {
			chunks = []string{"<i>(empty message)</i>"}
		}
		for _, chunk := range chunks {
			msg := tgbotapi.NewMessage(chatID, chunk)
			msg.ParseMode = "HTML"
			if len(markups) > 0 && markups[0] != nil {
				msg.ReplyMarkup = markups[0]
			}
			bot.Send(msg)
		}
		return chunks
	}

	chunks := sendChunk(bot, chatID, activeMsgID, text, markups...)
	if len(chunks) > 1 {
		for i := 1; i < len(chunks); i++ {
			msg := tgbotapi.NewMessage(chatID, chunks[i])
			msg.ParseMode = "HTML"
			if len(markups) > 0 && markups[0] != nil {
				msg.ReplyMarkup = markups[0]
			}
			bot.Send(msg)
		}
	}
	return chunks
}

// ---------------------------------------------------------------------------
// Inbound Rich Message Interception & Ingestion Pipeline (Issue #362)
// ---------------------------------------------------------------------------

var (
	attachedRichMu     sync.RWMutex
	attachedRich       = make(map[*tgbotapi.Message]string)
	attachedRichKeys   []*tgbotapi.Message
	maxAttachedRichCap = 2048
)

// AttachRichMessage registers an inbound or forwarded rich message text with a Message pointer,
// and ensures msg.Text is populated if currently empty.
func AttachRichMessage(msg *tgbotapi.Message, text string) {
	if msg == nil || text == "" {
		return
	}
	if msg.Text == "" {
		msg.Text = text
	}
	attachedRichMu.Lock()
	defer attachedRichMu.Unlock()

	if _, exists := attachedRich[msg]; !exists {
		if len(attachedRichKeys) >= maxAttachedRichCap {
			oldest := attachedRichKeys[0]
			attachedRichKeys = attachedRichKeys[1:]
			delete(attachedRich, oldest)
		}
		attachedRichKeys = append(attachedRichKeys, msg)
	}
	attachedRich[msg] = text
}

// GetAttachedRichMessage retrieves the attached rich message text for a Message pointer.
func GetAttachedRichMessage(msg *tgbotapi.Message) (string, bool) {
	if msg == nil {
		return "", false
	}
	attachedRichMu.RLock()
	defer attachedRichMu.RUnlock()
	val, ok := attachedRich[msg]
	if ok && val != "" {
		return val, true
	}
	return "", false
}

// ExtractRichMessageText extracts markdown or text content from a raw JSON rich_message payload.
// Supports both structured JSON objects ({"markdown": "...", "text": "...", "thinking": {...}})
// and raw JSON string primitives.
func ExtractRichMessageText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	// 1. Check if raw payload is a JSON string literal
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && strings.TrimSpace(str) != "" {
		return strings.TrimSpace(str)
	}

	// 2. Structured rich message payload
	var obj struct {
		Markdown string `json:"markdown"`
		Text     string `json:"text"`
		Thinking *struct {
			Text string `json:"text"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if strings.TrimSpace(obj.Markdown) != "" {
			return strings.TrimSpace(obj.Markdown)
		}
		if strings.TrimSpace(obj.Text) != "" {
			return strings.TrimSpace(obj.Text)
		}
		if obj.Thinking != nil && strings.TrimSpace(obj.Thinking.Text) != "" {
			return strings.TrimSpace(obj.Thinking.Text)
		}
	}
	return ""
}

// extractContainerRichText searches a container (like forward_origin or forward_from) for rich_message.
func extractContainerRichText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var container struct {
		RichMessage json.RawMessage `json:"rich_message"`
	}
	if err := json.Unmarshal(raw, &container); err == nil && len(container.RichMessage) > 0 {
		return ExtractRichMessageText(container.RichMessage)
	}
	return ""
}

// EnrichMessageFromJSON inspects raw message JSON for rich_message fields
// (at the message root, forward_origin, forward_from, or reply_to_message)
// and attaches the extracted content to the message and its children.
func EnrichMessageFromJSON(msg *tgbotapi.Message, rawMsg json.RawMessage) {
	if msg == nil || len(rawMsg) == 0 || string(rawMsg) == "null" {
		return
	}

	var fields struct {
		RichMessage     json.RawMessage `json:"rich_message"`
		ForwardOrigin   json.RawMessage `json:"forward_origin"`
		ForwardFrom     json.RawMessage `json:"forward_from"`
		ForwardFromChat json.RawMessage `json:"forward_from_chat"`
		ReplyToMessage  json.RawMessage `json:"reply_to_message"`
	}
	if err := json.Unmarshal(rawMsg, &fields); err != nil {
		return
	}

	// 1. Direct rich_message on message
	if richText := ExtractRichMessageText(fields.RichMessage); richText != "" {
		AttachRichMessage(msg, richText)
	}

	// 2. Forwarded rich_message
	if msg.Text == "" {
		if originText := extractContainerRichText(fields.ForwardOrigin); originText != "" {
			AttachRichMessage(msg, originText)
		} else if fwFromText := extractContainerRichText(fields.ForwardFrom); fwFromText != "" {
			AttachRichMessage(msg, fwFromText)
		} else if fwChatText := extractContainerRichText(fields.ForwardFromChat); fwChatText != "" {
			AttachRichMessage(msg, fwChatText)
		}
	}

	// 3. ReplyToMessage rich_message
	if msg.ReplyToMessage != nil && len(fields.ReplyToMessage) > 0 {
		EnrichMessageFromJSON(msg.ReplyToMessage, fields.ReplyToMessage)
	}
}

// ParseMessageFromJSON deserializes a Telegram Message from raw JSON and extracts rich_message content.
func ParseMessageFromJSON(data []byte) (*tgbotapi.Message, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, fmt.Errorf("empty message json")
	}

	var wrapper struct {
		Ok     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	targetData := data
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Ok && len(wrapper.Result) > 0 {
		targetData = wrapper.Result
	}

	var msg tgbotapi.Message
	if err := json.Unmarshal(targetData, &msg); err != nil {
		return nil, err
	}
	EnrichMessageFromJSON(&msg, targetData)
	return &msg, nil
}

// ParseUpdateFromJSON deserializes a single Telegram Update from raw JSON and extracts rich_message content.
func ParseUpdateFromJSON(data []byte) (tgbotapi.Update, error) {
	if len(data) == 0 || string(data) == "null" {
		return tgbotapi.Update{}, fmt.Errorf("empty update json")
	}

	var wrapper struct {
		Ok     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	targetData := data
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Ok && len(wrapper.Result) > 0 {
		targetData = wrapper.Result
	}

	var update tgbotapi.Update
	if err := json.Unmarshal(targetData, &update); err != nil {
		return tgbotapi.Update{}, err
	}

	var rawFields struct {
		Message           json.RawMessage `json:"message"`
		EditedMessage     json.RawMessage `json:"edited_message"`
		ChannelPost       json.RawMessage `json:"channel_post"`
		EditedChannelPost json.RawMessage `json:"edited_channel_post"`
	}
	if err := json.Unmarshal(targetData, &rawFields); err == nil {
		if update.Message != nil && len(rawFields.Message) > 0 {
			EnrichMessageFromJSON(update.Message, rawFields.Message)
		}
		if update.EditedMessage != nil && len(rawFields.EditedMessage) > 0 {
			EnrichMessageFromJSON(update.EditedMessage, rawFields.EditedMessage)
		}
		if update.ChannelPost != nil && len(rawFields.ChannelPost) > 0 {
			EnrichMessageFromJSON(update.ChannelPost, rawFields.ChannelPost)
		}
		if update.EditedChannelPost != nil && len(rawFields.EditedChannelPost) > 0 {
			EnrichMessageFromJSON(update.EditedChannelPost, rawFields.EditedChannelPost)
		}
	}

	return update, nil
}

// ParseUpdatesFromJSON deserializes a slice of Telegram Updates from raw JSON and extracts rich_message content.
func ParseUpdatesFromJSON(data []byte) ([]tgbotapi.Update, error) {
	if len(data) == 0 || string(data) == "null" {
		return []tgbotapi.Update{}, nil
	}

	var wrapper struct {
		Ok     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	targetData := data
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Ok && len(wrapper.Result) > 0 {
		targetData = wrapper.Result
	}

	var updates []tgbotapi.Update
	if err := json.Unmarshal(targetData, &updates); err != nil {
		return nil, err
	}

	var rawUpdates []json.RawMessage
	if err := json.Unmarshal(targetData, &rawUpdates); err == nil && len(rawUpdates) == len(updates) {
		for i, raw := range rawUpdates {
			var rawFields struct {
				Message           json.RawMessage `json:"message"`
				EditedMessage     json.RawMessage `json:"edited_message"`
				ChannelPost       json.RawMessage `json:"channel_post"`
				EditedChannelPost json.RawMessage `json:"edited_channel_post"`
			}
			if err := json.Unmarshal(raw, &rawFields); err == nil {
				if updates[i].Message != nil && len(rawFields.Message) > 0 {
					EnrichMessageFromJSON(updates[i].Message, rawFields.Message)
				}
				if updates[i].EditedMessage != nil && len(rawFields.EditedMessage) > 0 {
					EnrichMessageFromJSON(updates[i].EditedMessage, rawFields.EditedMessage)
				}
				if updates[i].ChannelPost != nil && len(rawFields.ChannelPost) > 0 {
					EnrichMessageFromJSON(updates[i].ChannelPost, rawFields.ChannelPost)
				}
				if updates[i].EditedChannelPost != nil && len(rawFields.EditedChannelPost) > 0 {
					EnrichMessageFromJSON(updates[i].EditedChannelPost, rawFields.EditedChannelPost)
				}
			}
		}
	}

	return updates, nil
}

// getUpdatesWithRichMessage requests Telegram updates and enriches incoming updates with rich_message payloads.
func getUpdatesWithRichMessage(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error) {
	if bot == nil {
		return nil, fmt.Errorf("bot instance is nil")
	}
	resp, err := bot.Request(config)
	if err != nil {
		return nil, err
	}
	return ParseUpdatesFromJSON(resp.Result)
}
