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
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

const (
	// RichMessageThreshold is the character threshold (in runes) above which responses switch to sendRichMessage.
	RichMessageThreshold = 3000

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
	// Tier1ClassicBubble: < 3000 runes, no tables, no thoughts -> classic sendMessage (HTML).
	Tier1ClassicBubble DeliveryTier = 1

	// Tier2RichArticle: 3000..32768 runes, or contains tables/thoughts -> monolithic sendRichMessage.
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
	placeholders := make(map[string]string)

	normText := strings.ReplaceAll(text, "\r\n", "\n")
	normText = strings.ReplaceAll(normText, "\r", "\n")
	if strings.Count(normText, "```")%2 != 0 {
		normText += "\n```"
	}

	shielded := reTGFenced.ReplaceAllStringFunc(normText, func(m string) string {
		token := fmt.Sprintf("@@TGCODEPLACEHOLDER%s@@", strings.ReplaceAll(uuid.New().String(), "-", ""))
		placeholders[token] = m
		return token
	})

	shielded = reTGInline.ReplaceAllStringFunc(shielded, func(m string) string {
		token := fmt.Sprintf("@@TGCODEPLACEHOLDER%s@@", strings.ReplaceAll(uuid.New().String(), "-", ""))
		placeholders[token] = m
		return token
	})

	return shielded, placeholders
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

// DetermineDeliveryTier evaluates which tier should be used to deliver the response.
func DetermineDeliveryTier(text string) DeliveryTier {
	runeLen := utf8.RuneCountInString(text)
	if runeLen > MaxRichMessageLength {
		return Tier3MarkdownArtifact
	}
	if runeLen >= RichMessageThreshold || HasMarkdownTable(text) || HasThoughts(text) || utf8.RuneCountInString(MarkdownToTelegramHTML(text)) > ClassicMessageLimit {
		return Tier2RichArticle
	}
	return Tier1ClassicBubble
}

// ShouldUseRichMessage evaluates whether a message warrants routing to sendRichMessage.
// Returns true for Tier2RichArticle (>= 3000 runes or tables/thoughts/large HTML, up to 32768 runes).
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

// sendRichMessage makes a request to the Telegram Bot API sendRichMessage endpoint.
func sendRichMessage(bot *tgbotapi.BotAPI, chatID int64, input any, markups ...*tgbotapi.InlineKeyboardMarkup) (*tgbotapi.Message, error) {
	if bot == nil {
		return nil, fmt.Errorf("bot instance is nil")
	}

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
		return nil, fmt.Errorf("unsupported input type for sendRichMessage: %T", input)
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
		return nil, fmt.Errorf("failed to marshal rich_message: %w", err)
	}

	params := make(tgbotapi.Params)
	params["chat_id"] = strconv.FormatInt(chatID, 10)
	params["rich_message"] = string(richPayload)

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

// sendAdaptiveResponse routes outbound responses across the Tri-Modal Delivery Architecture:
// Tier 1 (Classic Bubble): < 3000 runes, no tables, no thoughts -> classic sendMessage (HTML).
// Tier 2 (Rich Article): 3000..32768 runes, or contains tables/thoughts/large HTML -> monolithic sendRichMessage.
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
		_, err := sendRichMessage(bot, chatID, text, markups...)
		if err == nil {
			if activeMsgID != 0 {
				delMsg := tgbotapi.NewDeleteMessage(chatID, activeMsgID)
				_, _ = bot.Send(delMsg)
			}
			return []string{text}
		}
		log.Printf("sendRichMessage failed for chatID %d (len %d), falling back to classic cascade: %v", chatID, len(text), err)

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
