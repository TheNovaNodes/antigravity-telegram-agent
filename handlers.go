package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

var validSessionIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// isValidSessionID checks if a session ID is a safe identifier without path traversal characters.
func isValidSessionID(sid string) bool {
	sid = strings.TrimSpace(sid)
	if sid == "" || len(sid) > 128 {
		return false
	}
	return validSessionIDRegex.MatchString(sid)
}

// safePrefix returns up to maxRunes characters from a string without slicing out of bounds.
func safePrefix(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// truncateUTF8Bytes truncates a string to at most maxBytes without severing multi-byte UTF-8 runes.
func truncateUTF8Bytes(s string, maxBytes int) string {
	if len([]byte(s)) <= maxBytes {
		return s
	}
	b := []byte(s)[:maxBytes]
	for !utf8.Valid(b) && len(b) > 0 {
		b = b[:len(b)-1]
	}
	return string(b)
}

var (
	questionOptionsMu sync.RWMutex
	questionOptions   = make(map[string]string)
)

// storeQuestionOption safely registers an agent question option and returns safe callback data (<= 64 bytes).
func storeQuestionOption(opt string) string {
	cbData := "ans:" + opt
	if len([]byte(cbData)) <= 64 {
		return cbData
	}
	questionOptionsMu.Lock()
	defer questionOptionsMu.Unlock()
	if len(questionOptions) > 1000 {
		for k := range questionOptions {
			delete(questionOptions, k)
			if len(questionOptions) <= 500 {
				break
			}
		}
	}
	id := uuid.New().String()[:8]
	cbKey := "ans_id:" + id
	questionOptions[cbKey] = opt
	return cbKey
}

// getQuestionOption retrieves the full question answer text from callback data.
func getQuestionOption(data string) (string, bool) {
	if strings.HasPrefix(data, "ans:") {
		return strings.TrimPrefix(data, "ans:"), true
	}
	if strings.HasPrefix(data, "ans_id:") {
		questionOptionsMu.RLock()
		defer questionOptionsMu.RUnlock()
		if opt, ok := questionOptions[data]; ok {
			return opt, true
		}
	}
	return "", false
}

// handleStartCommand renders the status dashboard with session metrics and quick action keyboard.
func handleStartCommand(bot *tgbotapi.BotAPI, chatID int64, botName string, user User) {
	sessionTitle := "(empty)"
	stepsCount := 0
	uptimeStr := "0 m"

	brainDir := getBrainDir()
	if isValidSessionID(user.SessionID) {
		sessionDir := filepath.Join(brainDir, user.SessionID)

		titleFile := filepath.Join(sessionDir, ".title")
		if b, err := os.ReadFile(titleFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			sessionTitle = string(bytes.TrimSpace(b))
		}

		transcriptFile := filepath.Join(sessionDir, ".system_generated", "logs", "transcript.jsonl")
		if f, err := os.Open(transcriptFile); err == nil {
			scanner := bufio.NewScanner(f)
			var firstStepTime time.Time
			for scanner.Scan() {
				stepsCount++
				if stepsCount == 1 {
					var step map[string]interface{}
					if json.Unmarshal([]byte(scanner.Text()), &step) == nil {
						if createdRaw, ok := step["created_at"].(string); ok {
							if t, err := time.Parse(time.RFC3339, createdRaw); err == nil {
								firstStepTime = t
							}
						}
					}
				}
			}
			f.Close()
			if !firstStepTime.IsZero() {
				dur := time.Since(firstStepTime)
				if dur.Hours() >= 1 {
					uptimeStr = fmt.Sprintf("%d h %d m", int(dur.Hours()), int(dur.Minutes())%60)
				} else {
					uptimeStr = fmt.Sprintf("%d m", int(dur.Minutes()))
				}
			}
			if sessionTitle == "(empty)" && stepsCount > 0 {
				sessionTitle = "Session active"
			}
		}
	}

	voiceStatus := "🔇 Disabled"
	if user.VoiceReply {
		voiceStatus = "🎙 Enabled"
	}

	respText := fmt.Sprintf(`🛰 *Agent Terminal*
🤖 *Agent:* `+"`@%s`"+`
🟢 *Status:* Awaiting task

📂 *CWD:* `+"`%s`"+`
🧠 *Model:* `+"`%s`"+`
🎙 *Voice Reply:* %s

📋 *Session:* %s
⏱ *Uptime:* %s
👣 *Steps:* %d`, botName, user.Workspace, user.Model, voiceStatus, sessionTitle, uptimeStr, stepsCount)

	m := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🧠 Model", "cmd:model"),
			tgbotapi.NewInlineKeyboardButtonData("📊 Usage", "cmd:usage"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🧼 Clear", "cmd:clear"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 Sessions", "cmd:resume"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ Rename", "cmd:rename"),
			tgbotapi.NewInlineKeyboardButtonData("📄 Export", "cmd:export"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🆘 Help", "cmd:help"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, respText)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = m
	bot.Send(msg)
}

// handleResumeCommand displays an inline list of recent user sessions for resumption.
func handleResumeCommand(bot *tgbotapi.BotAPI, chatID, userID int64, db *sql.DB) {
	if db == nil {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 No session history available."))
		return
	}
	brainDir := getBrainDir()
	dbRows, err := db.Query("SELECT session_id FROM session_history WHERE user_id = ? ORDER BY created_at DESC LIMIT 20", userID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to read session history"))
		return
	}
	var validSessionIDs []string
	for dbRows.Next() {
		var sid string
		if err := dbRows.Scan(&sid); err == nil {
			validSessionIDs = append(validSessionIDs, sid)
		}
	}
	dbRows.Close()

	type convInfo struct {
		ID      string
		ModTime time.Time
		Title   string
	}
	var convs []convInfo

	for _, sid := range validSessionIDs {
		transcript := filepath.Join(brainDir, sid, ".system_generated", "logs", "transcript.jsonl")
		stat, err := os.Stat(transcript)
		if err != nil {
			continue
		}
		f, err := os.Open(transcript)
		if err != nil {
			continue
		}
		title := ""
		titleFile := filepath.Join(brainDir, sid, ".title")
		if b, err := os.ReadFile(titleFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
			title = string(bytes.TrimSpace(b))
			if len(title) > 45 {
				title = title[:42] + "..."
			}
		} else {
			scanner := bufio.NewScanner(f)
			if scanner.Scan() {
				var step map[string]interface{}
				if json.Unmarshal([]byte(scanner.Text()), &step) == nil {
					content, _ := step["content"].(string)
					start := strings.Index(content, "<USER_REQUEST>")
					end := strings.Index(content, "</USER_REQUEST>")
					if start >= 0 && end > start {
						inner := strings.TrimSpace(content[start+14 : end])
						for strings.Contains(inner, "<") {
							tagStart := strings.Index(inner, "<")
							tagEnd := strings.Index(inner, ">")
							if tagEnd > tagStart {
								inner = inner[:tagStart] + inner[tagEnd+1:]
							} else {
								break
							}
						}
						inner = strings.TrimSpace(inner)
						lines := strings.SplitN(inner, "\n", 2)
						title = strings.TrimSpace(lines[0])
						if len(title) > 45 {
							title = title[:42] + "..."
						}
					}
				}
			}
		}
		f.Close()
		if title == "" {
			title = "(empty)"
		}
		convs = append(convs, convInfo{ID: sid, ModTime: stat.ModTime(), Title: title})
	}

	for i := 0; i < len(convs); i++ {
		for j := i + 1; j < len(convs); j++ {
			if convs[j].ModTime.After(convs[i].ModTime) {
				convs[i], convs[j] = convs[j], convs[i]
			}
		}
	}

	if len(convs) > 8 {
		convs = convs[:8]
	}

	if len(convs) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 No previous sessions found."))
		return
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, c := range convs {
		label := fmt.Sprintf("🕐 %s — %s", c.ModTime.Format("02.01 15:04"), c.Title)
		cbData := "resume:" + c.ID
		cbData = truncateUTF8Bytes(cbData, 64)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, cbData),
		))
	}
	m := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, "🔄 *Select a session to resume:*")
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = m
	bot.Send(msg)
}

// handleModelCommand presents dynamic model choices via inline buttons.
func handleModelCommand(bot *tgbotapi.BotAPI, chatID int64) {
	respText := "🧠 Select a model:"
	modelsMu.RLock()
	cachedModels := make([]AgyModel, len(availableModels))
	copy(cachedModels, availableModels)
	modelsMu.RUnlock()

	var rows [][]tgbotapi.InlineKeyboardButton
	if len(cachedModels) == 0 {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.7 Flash High", "model:gemini-3.7-flash-high")))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🧠 3.1 Pro High", "model:gemini-3.1-pro-high")))
	} else {
		for _, m := range cachedModels {
			label := fmt.Sprintf("%s %s", m.Emoji, m.Name)
			callbackData := "model:" + m.ID
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, callbackData)))
		}
	}
	m := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, respText)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = m
	bot.Send(msg)
}

// handleUsageCommand retrieves current token quota usage from the underlying Antigravity CLI.
func handleUsageCommand(bot *tgbotapi.BotAPI, chatID int64) {
	agyPath := getAgyPath()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agyPath, "--print", "/usage")
	out, err := cmd.CombinedOutput()
	var respText string
	if err != nil {
		respText = "❌ Failed to get usage: " + err.Error()
	} else {
		respText = "📊 *Quota Usage:*\n```\n" + strings.TrimSpace(string(out)) + "\n```"
	}
	msg := tgbotapi.NewMessage(chatID, respText)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// handleHelpCommand outputs the command reference.
func handleHelpCommand(bot *tgbotapi.BotAPI, chatID int64) {
	respText := "🆘 *Command Reference:*\n\n" +
		"• /start - Show dashboard\n" +
		"• /model - Change LLM model\n" +
		"• /usage - Check API quota\n" +
		"• /clear - Clear context (reset session)\n" +
		"• /resume - Resume previous session\n" +
		"• /rename <name> - Rename current session\n" +
		"• /workspace <path> - Change working directory\n" +
		"• /export - Export conversation transcript to Markdown file\n" +
		"• /voice [on|off] - Toggle persistent voice responses\n\n" +
		"*Send any text or file to start the Agent.*"
	msg := tgbotapi.NewMessage(chatID, respText)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// handleExportCommand extracts the conversation steps from transcript.jsonl and sends a formatted Markdown file to the chat.
func handleExportCommand(bot *tgbotapi.BotAPI, chatID, userID int64, botName string, user User) {
	if !isValidSessionID(user.SessionID) {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 No active conversation session found to export."))
		return
	}
	brainDir := getBrainDir()
	sessionDir := filepath.Join(brainDir, user.SessionID)
	transcriptFile := filepath.Join(sessionDir, ".system_generated", "logs", "transcript.jsonl")

	f, err := os.Open(transcriptFile)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 No conversation transcript found for the current session."))
		return
	}
	defer f.Close()

	sessionTitle := "(untitled session)"
	titleFile := filepath.Join(sessionDir, ".title")
	if b, err := os.ReadFile(titleFile); err == nil && len(bytes.TrimSpace(b)) > 0 {
		sessionTitle = string(bytes.TrimSpace(b))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# 🛸 Agent Session Transcript: %s\n\n", sessionTitle))
	sb.WriteString(fmt.Sprintf("- **Agent:** `@%s`\n", botName))
	sb.WriteString(fmt.Sprintf("- **Session ID:** `%s`\n", user.SessionID))
	sb.WriteString(fmt.Sprintf("- **Model:** `%s`\n", user.Model))
	sb.WriteString(fmt.Sprintf("- **Workspace:** `%s`\n", user.Workspace))
	sb.WriteString(fmt.Sprintf("- **Exported At:** %s\n\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("---\n\n")

	scanner := bufio.NewScanner(f)
	stepNum := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var step map[string]interface{}
		if json.Unmarshal([]byte(line), &step) == nil {
			stepNum++
			source, _ := step["source"].(string)
			stepType, _ := step["type"].(string)
			createdAt, _ := step["created_at"].(string)
			content, _ := step["content"].(string)

			roleHeader := "### 🤖 Assistant"
			if source == "USER_EXPLICIT" || stepType == "USER_INPUT" {
				roleHeader = "### 👤 User"
				start := strings.Index(content, "<USER_REQUEST>")
				end := strings.Index(content, "</USER_REQUEST>")
				if start >= 0 && end > start {
					content = strings.TrimSpace(content[start+14 : end])
				}
			} else if source == "SYSTEM" {
				roleHeader = "### ⚙️ System"
			}

			timeStr := ""
			if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
				timeStr = fmt.Sprintf(" (%s)", t.Format("2006-01-02 15:04:05 UTC"))
			}

			sb.WriteString(fmt.Sprintf("%s%s\n\n", roleHeader, timeStr))
			if strings.TrimSpace(content) != "" {
				sb.WriteString(strings.TrimSpace(content) + "\n\n")
			}

			if tcs, ok := step["tool_calls"].([]interface{}); ok && len(tcs) > 0 {
				sb.WriteString("```json\n")
				for _, tc := range tcs {
					if tcBytes, err := json.MarshalIndent(tc, "", "  "); err == nil {
						sb.WriteString(string(tcBytes) + "\n")
					}
				}
				sb.WriteString("```\n\n")
			}
		}
	}

	if stepNum == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 Transcript is currently empty."))
		return
	}

	exportDir := filepath.Join(getAgentsDir(), botName, "scratch", "exports")
	os.MkdirAll(exportDir, 0755)
	safeFilename := fmt.Sprintf("session_%s.md", safePrefix(user.SessionID, 8))
	exportPath := filepath.Join(exportDir, safeFilename)

	if err := os.WriteFile(exportPath, []byte(sb.String()), 0644); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to generate export file: "+err.Error()))
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(exportPath))
	doc.Caption = fmt.Sprintf("📄 *Session Transcript Export*\n🏷 *Title:* %s\n👣 *Steps:* %d", sessionTitle, stepNum)
	doc.ParseMode = "Markdown"
	bot.Send(doc)
}

// handleTTSCommand converts text to speech using ElevenLabs and sends as audio.
func handleTTSCommand(bot *tgbotapi.BotAPI, chatID int64, text string) {
	ttsText := strings.TrimSpace(strings.TrimPrefix(text, "/tts"))
	if ttsText == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: `/tts <text>`"))
		return
	}
	if os.Getenv("ELEVENLABS_API_KEY") == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ ELEVENLABS_API_KEY environment variable is not set!"))
		return
	}

	msg := tgbotapi.NewMessage(chatID, "🎙 *Generating voice...*")
	msg.ParseMode = "Markdown"
	sentMsg, _ := bot.Send(msg)

	err := GenerateAndSendVoice(bot, chatID, ttsText)
	if sentMsg.MessageID != 0 {
		bot.Send(tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID))
	}

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ TTS Error: "+err.Error()))
	}
}

// handleVoiceToggleCommand enables or disables persistent voice replies for the user and syncs active sessions.
func handleVoiceToggleCommand(bot *tgbotapi.BotAPI, chatID, userID int64, text, botName string, user User, db *sql.DB) {
	arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "/voice")))
	newState := !user.VoiceReply
	if arg == "on" || arg == "1" || arg == "true" {
		newState = true
	} else if arg == "off" || arg == "0" || arg == "false" {
		newState = false
	}

	updateUserVoiceReply(db, userID, newState)

	// Sync active in-memory session if present
	sessionMu.Lock()
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, userID)
	if s, exists := globalSessions[sessionKey]; exists {
		s.mu.Lock()
		s.VoiceReply = newState
		s.mu.Unlock()
	}
	sessionMu.Unlock()

	statusStr := "❌ Disabled"
	if newState {
		statusStr = "✅ Enabled (Bot will reply with voice messages)"
	}
	msg := tgbotapi.NewMessage(chatID, "🎙 *Voice Reply Mode:* "+statusStr)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// handleWorkspaceCommand updates the user's agent office/workspace.
func handleWorkspaceCommand(bot *tgbotapi.BotAPI, chatID, userID int64, text, botName string, user User, db *sql.DB) {
	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 {
		msg := tgbotapi.NewMessage(chatID, "⚠️ Usage: `/workspace <absolute path>`\n📂 Current workspace: `"+user.Workspace+"`")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}
	newWS := strings.TrimSpace(parts[1])
	cleanPath := filepath.Clean(newWS)
	if !filepath.IsAbs(cleanPath) {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Error: path must be absolute (e.g. `/root/projects/app`)"))
		return
	}
	realPath, evalErr := filepath.EvalSymlinks(cleanPath)
	if evalErr != nil {
		realPath = cleanPath
	}

	projectsDir := getProjectsDir()
	agentsDir := getAgentsDir()
	botOffice := filepath.Join(agentsDir, botName)

	// Allowed workspaces: anywhere under PROJECTS_DIR or within the bot's own office (Fail-Closed)
	isAllowed := isPathUnderRoot(realPath, projectsDir) || isPathUnderRoot(realPath, botOffice)
	if !isAllowed {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Path must be under %s or %s", projectsDir, botOffice)))
		return
	}
	newWS = realPath

	if db != nil {
		_, err := db.Exec("UPDATE users SET workspace = ? WHERE user_id = ?", newWS, userID)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: "+err.Error()))
			return
		}
	}

	updateUserSession(db, userID, "")
	replaceSession(db, botName, user, "", user.Model, newWS, chatID)

	msg := tgbotapi.NewMessage(chatID, "📂 Target Lab (Workspace) changed to: `"+newWS+"`\n\n⚠️ *Warning:* Session restarted. All active background tasks and subagents were terminated.")
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// handleRenameCommand renames the current conversation in brain storage.
func handleRenameCommand(bot *tgbotapi.BotAPI, chatID int64, text, botName string, user User) {
	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 {
		msg := tgbotapi.NewMessage(chatID, "⚠️ Usage: `/rename <new name>`")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}
	newName := strings.TrimSpace(parts[1])
	newName = strings.ReplaceAll(newName, "\n", " ")
	newName = strings.ReplaceAll(newName, "\r", "")
	if r := []rune(newName); len(r) > 60 {
		newName = string(r[:60])
	}
	if newName == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Name cannot be empty."))
		return
	}

	session := getSession(botName, user, chatID)
	conv := session.GetConversation()
	if conv != "" && isValidSessionID(conv) {
		sessionDir := filepath.Join(getBrainDir(), conv)
		os.MkdirAll(sessionDir, 0755)
		titleFile := filepath.Join(sessionDir, ".title")
		err := os.WriteFile(titleFile, []byte(newName), 0644)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to save name: "+err.Error()))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "✅ Session renamed to: *"+newName+"*"))
		}
	} else {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ No active session to rename."))
	}
}

// handleClearCommand clears the active conversation context.
func handleClearCommand(bot *tgbotapi.BotAPI, chatID, userID int64, botName string, user User, db *sql.DB) {
	// For a fresh start, pass an empty conversation ID so agy starts cleanly without an uninitialized --conversation flag
	replaceSession(db, botName, user, "", user.Model, user.Workspace, chatID)
	updateUserSession(db, userID, "")
	respText := "🧼 Context cleared! Starting fresh session."
	msg := tgbotapi.NewMessage(chatID, respText)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// handleRefreshModelsCommand dynamically refreshes models cache.
func handleRefreshModelsCommand(bot *tgbotapi.BotAPI, chatID int64) {
	fetchModels()
	modelsMu.RLock()
	count := len(availableModels)
	modelsMu.RUnlock()
	bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Dynamically fetched %d models from agy.", count)))
}

// handleCommand parses and routes slash commands with strict token matching. Returns true if handled.
func handleCommand(bot *tgbotapi.BotAPI, chatID, userID int64, text, botName string, user User, db *sql.DB) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	botSuffix := "@" + strings.ToLower(botName)
	cmd = strings.TrimSuffix(cmd, botSuffix)

	switch cmd {
	case "/start":
		handleStartCommand(bot, chatID, botName, user)
		return true
	case "/resume":
		handleResumeCommand(bot, chatID, userID, db)
		return true
	case "/tts":
		handleTTSCommand(bot, chatID, text)
		return true
	case "/voice":
		handleVoiceToggleCommand(bot, chatID, userID, text, botName, user, db)
		return true
	case "/workspace":
		handleWorkspaceCommand(bot, chatID, userID, text, botName, user, db)
		return true
	case "/rename":
		handleRenameCommand(bot, chatID, text, botName, user)
		return true
	case "/export":
		handleExportCommand(bot, chatID, userID, botName, user)
		return true
	case "/usage":
		handleUsageCommand(bot, chatID)
		return true
	case "/help":
		handleHelpCommand(bot, chatID)
		return true
	case "/model":
		handleModelCommand(bot, chatID)
		return true
	case "/refresh_models":
		handleRefreshModelsCommand(bot, chatID)
		return true
	case "/clear":
		handleClearCommand(bot, chatID, userID, botName, user, db)
		return true
	default:
		return false
	}
}

// handleCallbackQuery processes all inline keyboard callback events.
func handleCallbackQuery(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, user User, botName string, db *sql.DB) {
	chatID := cb.Message.Chat.ID
	userID := cb.From.ID
	data := cb.Data

	bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	if strings.HasPrefix(data, "ans_id:") || strings.HasPrefix(data, "ans:") {
		if optText, ok := getQuestionOption(data); ok {
			handleMessagePayload(bot, chatID, userID, optText, botName, user, false, false, db)
		} else {
			msg := tgbotapi.NewMessage(chatID, "⚠️ Этот вариант ответа устарел или бот был перезагружен. Пожалуйста, отправьте ваш ответ текстом.")
			bot.Send(msg)
		}
		return
	}

	if strings.HasPrefix(data, "resume:") {
		convID := strings.TrimPrefix(data, "resume:")
		if !isValidSessionID(convID) {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Invalid session identifier format."))
			return
		}

		// BOLA Protection: Verify that the conversation belongs to the requesting user
		if user.SessionID != convID && !isSessionOwnedByUser(db, userID, convID) {
			log.Printf("[Bot %s] 🛑 BOLA VIOLATION: User %d attempted to resume unauthorized session %s", botName, userID, convID)
			bot.Send(tgbotapi.NewMessage(chatID, "⛔ Access Denied: You do not have permission to access this conversation session."))
			return
		}

		updateUserSession(db, userID, convID)
		session := replaceSession(db, botName, user, convID, user.Model, user.Workspace, chatID)

		select {
		case newID := <-session.InitChan:
			session.mu.Lock()
			session.Conversation = newID
			session.mu.Unlock()
			convID = newID
		case <-time.After(3 * time.Second):
		}

		respText := fmt.Sprintf("🔄 Resumed session!\n`%s`", safePrefix(convID, 8))
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	var respText string
	var markup *tgbotapi.InlineKeyboardMarkup

	if strings.HasPrefix(data, "model:") {
		newModel := strings.TrimPrefix(data, "model:")
		if err := updateUserModel(db, userID, newModel); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: "+err.Error()))
			return
		}

		user.Model = newModel
		replaceSession(db, botName, user, user.SessionID, newModel, user.Workspace, chatID)
		respText = "🧠 Model switched to `" + newModel + "`\n✨ *Context preserved!* Continuing existing session."
	} else if data == "cmd:status" {
		respText = fmt.Sprintf("📊 *Status:*\n\n*Bot:* `%s`\n*Workspace:* `%s`\n*Model:* `%s`\n*Session:* `%s`", botName, user.Workspace, user.Model, user.SessionID)
	} else if data == "cmd:model" {
		handleModelCommand(bot, chatID)
		return
	} else if data == "cmd:clear" {
		replaceSession(db, botName, user, "", user.Model, user.Workspace, chatID)
		updateUserSession(db, userID, "")
		respText = "🧼 Context cleared! Starting fresh session."
	} else if data == "cmd:usage" {
		handleUsageCommand(bot, chatID)
		return
	} else if data == "cmd:resume" {
		handleResumeCommand(bot, chatID, userID, db)
		return
	} else if data == "cmd:export" {
		handleExportCommand(bot, chatID, userID, botName, user)
		return
	} else if data == "cmd:rename" {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: `/rename <new name>`"))
		return
	} else if data == "cmd:help" {
		handleHelpCommand(bot, chatID)
		return
	}

	if respText != "" {
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		if markup != nil {
			msg.ReplyMarkup = markup
		}
		bot.Send(msg)
	}
}

// downloadTelegramMedia downloads incoming Telegram media attachment (file, photo, voice, audio).
func downloadTelegramMedia(bot *tgbotapi.BotAPI, chatID int64, fileID, ext, text, caption, botName string) (string, bool, error) {
	if fileID == "" {
		return text, false, nil
	}
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file."))
		return "", false, err
	}

	var fileURL string
	if strings.HasPrefix(file.FilePath, "http://") || strings.HasPrefix(file.FilePath, "https://") {
		fileURL = file.FilePath
	} else {
		fileURL = file.Link(bot.Token)
	}

	parsedURL, err := url.Parse(fileURL)
	if err != nil || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.Host == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Invalid file download URL."))
		return "", false, fmt.Errorf("invalid file URL: %s", fileURL)
	}
	downloadDir := filepath.Join(getAgentsDir(), botName, "scratch", "downloads")
	os.MkdirAll(downloadDir, 0755)
	safePath := filepath.Join(downloadDir, uuid.New().String()+ext)

	bot.Send(tgbotapi.NewMessage(chatID, "📥 Downloading file..."))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(fileURL)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file."))
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file from server."))
		return "", false, fmt.Errorf("file download failed with HTTP status %d", resp.StatusCode)
	}

	out, err := os.Create(safePath)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to save file to disk."))
		return "", false, err
	}
	defer out.Close()

	const maxUploadBytes = 100 << 20 // 100 MB
	if resp.ContentLength > maxUploadBytes {
		os.Remove(safePath)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ File too large (max 100 MB)"))
		return "", false, fmt.Errorf("file too large")
	}

	n, err := io.Copy(out, io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil || n > maxUploadBytes {
		os.Remove(safePath)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ File exceeds 100 MB limit"))
		return "", false, fmt.Errorf("file exceeds limit")
	}

	baseText := text
	if baseText == "" {
		baseText = caption
	}
	formattedText := fmt.Sprintf("[Attached File: file://%s]\n\n%s", safePath, baseText)
	return formattedText, true, nil
}

// handleMessagePayload handles queuing and writing user message streams into the agent's stdin pipe.
func handleMessagePayload(bot *tgbotapi.BotAPI, chatID, userID int64, text, botName string, user User, isVoice, downloadedFile bool, db *sql.DB) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	session := getSession(botName, user, chatID)

	session.mu.Lock()
	session.VoiceReply = isVoice || user.VoiceReply
	session.BotAPI = bot
	session.ChatID = chatID

	if !session.isAlive {
		session.ActiveMessageID = 0
		session.TextBuffer = ""
		session.mu.Unlock()
		session.start()
		session.mu.Lock()
	}

	activeMsgID := session.ActiveMessageID
	voiceReply := session.VoiceReply
	session.mu.Unlock()

	if !downloadedFile && bot != nil {
		if activeMsgID == 0 {
			msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
			msg.ParseMode = "Markdown"
			if sentMsg, err := bot.Send(msg); err == nil {
				session.mu.Lock()
				if session.ActiveMessageID == 0 {
					session.ActiveMessageID = sentMsg.MessageID
				}
				session.mu.Unlock()
			}
		} else {
			msg := tgbotapi.NewMessage(chatID, "⏳ _Message queued..._")
			msg.ParseMode = "Markdown"
			bot.Send(msg)
		}

		action := tgbotapi.ChatTyping
		if voiceReply {
			action = tgbotapi.ChatRecordVoice
		}
		bot.Send(tgbotapi.NewChatAction(chatID, action))
	}

	payload := map[string]interface{}{
		"event": "user",
		"message": map[string]string{
			"content": text,
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	payloadBytes = append(payloadBytes, '\n')

	session.mu.Lock()
	var err error
	if session.Stdin != nil {
		_, err = session.Stdin.Write(payloadBytes)
	} else {
		err = fmt.Errorf("agent stdin pipe is not available")
	}
	session.mu.Unlock()

	if err != nil {
		log.Printf("Stdin write failed for bot %s: %v, restarting session and notifying user", botName, err)
		session.Restart()
		session.mu.Lock()
		activeID := session.ActiveMessageID
		session.ActiveMessageID = 0
		session.mu.Unlock()
		if bot != nil {
			statusMsg := "⚠️ *Процесс агента был перезапущен.* Пожалуйста, отправьте сообщение повторно."
			if activeID != 0 {
				sendChunk(bot, chatID, activeID, statusMsg)
			} else {
				msg := tgbotapi.NewMessage(chatID, statusMsg)
				msg.ParseMode = "Markdown"
				bot.Send(msg)
			}
		}
		return
	}
}

type chatUpdateTask struct {
	bot    *tgbotapi.BotAPI
	update tgbotapi.Update
	db     *sql.DB
}

var (
	chatQueuesMu         sync.Mutex
	chatQueues           = make(map[int64]chan chatUpdateTask)
	chatQueueIdleTimeout = 5 * time.Minute
)

// dispatchUpdate routes an incoming update into a per-chat sequential FIFO queue to prevent race conditions.
func dispatchUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update, db *sql.DB) {
	var chatID int64
	if update.Message != nil && update.Message.Chat != nil {
		chatID = update.Message.Chat.ID
	} else if update.CallbackQuery != nil && update.CallbackQuery.Message != nil && update.CallbackQuery.Message.Chat != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
	}

	if chatID == 0 {
		go handleUpdate(bot, update, db)
		return
	}

	chatQueuesMu.Lock()
	ch, exists := chatQueues[chatID]
	if !exists {
		ch = make(chan chatUpdateTask, 100)
		chatQueues[chatID] = ch
		go func(cID int64, taskChan chan chatUpdateTask) {
			for {
				select {
				case task, ok := <-taskChan:
					if !ok {
						return
					}
					handleUpdate(task.bot, task.update, task.db)
				case <-time.After(chatQueueIdleTimeout):
					chatQueuesMu.Lock()
					if len(taskChan) == 0 {
						delete(chatQueues, cID)
						chatQueuesMu.Unlock()
						return
					}
					chatQueuesMu.Unlock()
				}
			}
		}(chatID, ch)
	}

	select {
	case ch <- chatUpdateTask{bot: bot, update: update, db: db}:
		chatQueuesMu.Unlock()
	default:
		chatQueuesMu.Unlock()
		// Fallback for extreme backlog: run in separate goroutine to avoid dropping updates
		go handleUpdate(bot, update, db)
	}
}

// handleUpdate is the primary router for incoming Telegram messages and inline callbacks.
func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update, db *sql.DB) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERED in handleUpdate] %v", r)
		}
	}()
	if update.Message == nil && update.CallbackQuery == nil {
		return
	}

	botName := bot.Self.UserName

	// 1. Process Callback Queries
	if update.CallbackQuery != nil {
		userID := update.CallbackQuery.From.ID
		user := getUser(db, userID, botName)
		handleCallbackQuery(bot, update.CallbackQuery, user, botName, db)
		return
	}

	// 2. Process Messages
	msg := update.Message
	chatID := msg.Chat.ID
	userID := msg.From.ID
	text := msg.Text
	caption := msg.Caption

	// Alias telegram-safe commands to agy commands
	if strings.HasPrefix(text, "/grill_me") {
		text = strings.Replace(text, "/grill_me", "/grill-me", 1)
	} else if strings.HasPrefix(text, "/teamwork_preview") {
		text = strings.Replace(text, "/teamwork_preview", "/teamwork-preview", 1)
	}

	var fileID, ext string
	isVoice := false
	if msg.Document != nil {
		fileID = msg.Document.FileID
		ext = filepath.Ext(msg.Document.FileName)
		if ext == "" {
			ext = ".bin"
		}
	} else if len(msg.Photo) > 0 {
		fileID = msg.Photo[len(msg.Photo)-1].FileID
		ext = ".jpg"
	} else if msg.Voice != nil {
		fileID = msg.Voice.FileID
		ext = ".ogg"
		isVoice = true
	} else if msg.Audio != nil {
		fileID = msg.Audio.FileID
		ext = ".mp3"
	}

	if text == "" && caption == "" && fileID == "" {
		bot.Request(tgbotapi.NewMessage(chatID, "⚠️ Sticker/contact/location не поддерживается. Отправьте текст, фото, документ или голосовое."))
		return
	}

	user := getUser(db, userID, botName)

	downloadedFile := false
	if fileID != "" {
		formattedText, isFile, err := downloadTelegramMedia(bot, chatID, fileID, ext, text, caption, botName)
		if err != nil {
			return
		}
		text = formattedText
		downloadedFile = isFile
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	// 3. Process Slash Commands
	if handleCommand(bot, chatID, userID, text, botName, user, db) {
		return
	}

	// 4. Process Normal Text & Media Payloads
	handleMessagePayload(bot, chatID, userID, text, botName, user, isVoice, downloadedFile, db)
}

// sendChunk safely breaks a large text into valid HTML chunks and sends them sequentially.
func sendChunk(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string) []string {
	log.Printf("sendChunk called for chatID %d, msgID %d, text len %d", chatID, messageID, len(text))
	chunks := SplitHTMLChunks(MarkdownToTelegramHTML(text), 4000)
	chunkToEdit := chunks[0]
	if len(chunks) > 1 && strings.Contains(text, "⏳") {
		chunkToEdit += "\n\n<i>[Truncated while typing...]</i>"
	}

	if bot == nil {
		return chunks
	}

	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, chunkToEdit)
	editMsg.ParseMode = "HTML"
	_, err := bot.Send(editMsg)
	if err != nil && err.Error() != "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message" {
		log.Printf("Edit error: %v, falling back to new message", err)
		newMsg := tgbotapi.NewMessage(chatID, chunkToEdit)
		newMsg.ParseMode = "HTML"
		bot.Send(newMsg)
	}
	return chunks
}

// CleanOldFiles removes files older than maxAge in the target directory.
func CleanOldFiles(dir string, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err == nil {
				cleaned++
			}
		}
	}
	return cleaned
}

// CleanMediaAndExports iterates over all agent workspaces and cleans up old temporary downloads and exports.
func CleanMediaAndExports(maxAge time.Duration) int {
	agentsDir := getAgentsDir()
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return 0
	}
	total := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		downloadsDir := filepath.Join(agentsDir, entry.Name(), "scratch", "downloads")
		total += CleanOldFiles(downloadsDir, maxAge)

		exportsDir := filepath.Join(agentsDir, entry.Name(), "scratch", "exports")
		total += CleanOldFiles(exportsDir, maxAge)
	}
	return total
}

// StartDiskCleanupWorker runs a background timer to periodically clean old media downloads and exports.
func StartDiskCleanupWorker(interval, maxAge time.Duration, stopChan <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				CleanMediaAndExports(maxAge)
			}
		}
	}()
}
