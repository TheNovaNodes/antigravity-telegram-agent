package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	// ClassicMessageLimit is the maximum character limit for classic Telegram sendMessage (HTML/Markdown).
	ClassicMessageLimit = 4000

	// MaxRichMessageLength is the hard limit for Telegram sendRichMessage format (32,768 UTF-8 characters).
	MaxRichMessageLength = 32768
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
var (
	thoughtRegex = regexp.MustCompile(`(?is)<(?:thought|think|thinking)>(.*?)(?:</(?:thought|think|thinking)>|$)`)
)

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

// ShouldUseRichMessage evaluates whether a message warrants routing to sendRichMessage.
// Returns true if character count (runes) is between 4,001 and 32,768, or if the text
// contains a Markdown table and fits within the 32,768 rune limit.
// Content exceeding 32,768 runes routes to SplitHTMLChunks cascade to avoid losing data.
func ShouldUseRichMessage(text string) bool {
	runeLen := utf8.RuneCountInString(text)
	if runeLen > MaxRichMessageLength {
		return false
	}
	if runeLen > ClassicMessageLimit {
		return true
	}
	return HasMarkdownTable(text)
}

// ExtractThinkingAndMarkdown extracts thinking / CoT blocks from raw model output,
// returning clean markdown and an optional RichBlockThinking struct.
func ExtractThinkingAndMarkdown(text string) (string, *RichBlockThinking) {
	var thoughts []string
	cleanText := thoughtRegex.ReplaceAllStringFunc(text, func(m string) string {
		subs := thoughtRegex.FindStringSubmatch(m)
		if len(subs) > 1 {
			t := strings.TrimSpace(subs[1])
			if t != "" {
				thoughts = append(thoughts, t)
			}
		}
		return ""
	})

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

// sendAdaptiveResponse sends messages via sendRichMessage when length > 4000 or containing tables,
// gracefully falling back to SplitHTMLChunks + sendMessage upon failure.
func sendAdaptiveResponse(bot *tgbotapi.BotAPI, chatID int64, activeMsgID int, text string, markups ...*tgbotapi.InlineKeyboardMarkup) []string {
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

	if ShouldUseRichMessage(text) {
		_, err := sendRichMessage(bot, chatID, text, markups...)
		if err == nil {
			if activeMsgID != 0 {
				delMsg := tgbotapi.NewDeleteMessage(chatID, activeMsgID)
				_, _ = bot.Send(delMsg)
			}
			return []string{text}
		}
		log.Printf("sendRichMessage failed for chatID %d (len %d), falling back to classic cascade: %v", chatID, len(text), err)
	}

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
