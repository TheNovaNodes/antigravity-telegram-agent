package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// User represents the User data structure.
type User struct {
	ID           int64
	Workspace    string
	Model        string
	IsFirstStart bool
	SessionID    string
}

// AgySession represents the AgySession data structure.
type AgySession struct {
	BotName      string
	Model        string
	Workspace    string
	Conversation string
	UseContinue  bool
	Cmd          *exec.Cmd
	Stdin        io.WriteCloser
	StdoutScanner *bufio.Scanner
	mu           sync.Mutex
	BotAPI       *tgbotapi.BotAPI
	ChatID       int64
	ActiveMessageID int
	TextBuffer   string
	LastEdit     time.Time
}

func initDB(botName string) *sql.DB {
	dbPath := fmt.Sprintf("sessions_%s.db", botName)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db %s: %v", dbPath, err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '/root',
		model TEXT DEFAULT 'gemini-3.1-pro-high',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL
	)`)
	if err != nil {
		log.Fatalf("Failed to create table in %s: %v", dbPath, err)
	}
	return db
}

func getUser(db *sql.DB, userID int64) User {
	var u User
	err := db.QueryRow("SELECT user_id, workspace, model, is_first_start, session_id FROM users WHERE user_id = ?", userID).Scan(
		&u.ID, &u.Workspace, &u.Model, &u.IsFirstStart, &u.SessionID)
	if err == sql.ErrNoRows {
		u = User{
			ID:           userID,
			Workspace:    "/root",
			Model:        "gemini-3.1-pro-high",
			IsFirstStart: true,
			SessionID:    uuid.New().String(),
		}
		db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
			u.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)
	} else if u.SessionID == "" {
		u.SessionID = uuid.New().String()
		db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", u.SessionID, u.ID)
	}
	return u
}

func updateUserSession(db *sql.DB, userID int64, sessionID string) {
	db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)
}

func updateUserModel(db *sql.DB, userID int64, model string) {
	db.Exec("UPDATE users SET model = ? WHERE user_id = ?", model, userID)
}

func loadAllowedAdmins() map[int64]bool {
	allowed := make(map[int64]bool)
	env := os.Getenv("ALLOWED_ADMIN_IDS")
	if env == "" {
		log.Println("⚠️ ALLOWED_ADMIN_IDS is not set!")
		return allowed
	}
	for _, idStr := range strings.Split(env, ",") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		var id int64
		fmt.Sscanf(idStr, "%d", &id)
		allowed[id] = true
	}
	return allowed
}

var globalSessions = make(map[string]*AgySession)
var sessionMu sync.Mutex

func getSession(botName string, user User) *AgySession {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	session, exists := globalSessions[botName]
	if exists && session.Model == user.Model && session.Workspace == user.Workspace {
		// check if process is alive
		if session.Cmd != nil && session.Cmd.ProcessState == nil {
			return session
		}
	}

	if exists && session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}

	session = &AgySession{
		BotName:      botName,
		Model:        user.Model,
		Workspace:    user.Workspace,
		Conversation: user.SessionID,
	}

	session.start()
	globalSessions[botName] = session
	return session
}

func (s *AgySession) start() {
	args := []string{
		"--model", s.Model,
		"--dangerously-skip-permissions",
		"--mode", "accept-edits",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "1h",
		"--add-dir", "/root/.agents/common",
		"--add-dir", "/root/.agents/" + s.BotName,
		"--add-dir", s.Workspace,
	}
	if s.UseContinue {
		args = append(args, "--continue")
	} else if s.Conversation != "" {
		args = append(args, "--conversation", s.Conversation)
	}

	s.Cmd = exec.Command("/tmp/agy_wrapper.sh", args...)
	stdin, _ := s.Cmd.StdinPipe()
	stdout, _ := s.Cmd.StdoutPipe()
	s.Stdin = stdin
	s.StdoutScanner = bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	s.StdoutScanner.Buffer(buf, 10*1024*1024) // 10MB max token size
	s.Cmd.Start()
	
	// Streaming throttler loop
	go func() {
		var lastSent string
		var lastSentTime time.Time
		for {
			s.mu.Lock()
			if s.Cmd == nil {
				s.mu.Unlock()
				break
			}
			currentText := s.TextBuffer
			activeMsgID := s.ActiveMessageID
			botAPI := s.BotAPI
			chatID := s.ChatID
			s.mu.Unlock()
			
			if currentText != "" && currentText != lastSent && botAPI != nil {
                if activeMsgID == 0 {
                    msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
                    msg.ParseMode = "Markdown"
                    if sentMsg, err := botAPI.Send(msg); err == nil {
                        s.mu.Lock()
                        s.ActiveMessageID = sentMsg.MessageID
                        activeMsgID = sentMsg.MessageID
                        s.mu.Unlock()
                    }
                }
				if activeMsgID != 0 && time.Since(lastSentTime) > 1000*time.Millisecond {
					sendChunk(botAPI, chatID, activeMsgID, currentText+"\n\n*⏳ Typing...*")
					lastSent = currentText
					lastSentTime = time.Now()
				}
			}
			time.Sleep(100 * time.Millisecond) // Fast polling, rate limited sending
		}
	}()

	go s.readStdoutLoop()
	go func() {
		s.Cmd.Wait()
		s.mu.Lock()
		s.Cmd = nil
		s.mu.Unlock()
	}()
}

// Restart performs the Restart method.
func (s *AgySession) Restart() {
	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
	}
	s.start()
}

func sendArtifacts(bot *tgbotapi.BotAPI, chatID int64, text string) {
	re := regexp.MustCompile(`\(file://(.*?)\)`)
	matches := re.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 {
			filePath := match[1]
			if decoded, err := url.PathUnescape(filePath); err == nil {
				filePath = decoded
			}
			if _, err := os.Stat(filePath); err == nil {
				doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
				doc.Caption = "📦 Artifact: " + filepath.Base(filePath)
				bot.Send(doc)
			}
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update, db *sql.DB) {
	if update.Message == nil && update.CallbackQuery == nil {
		return
	}

	var chatID int64
	var userID int64
	var text string
	var caption string
	var fileID string
	var ext string

	if update.Message != nil {
		chatID = update.Message.Chat.ID
		userID = update.Message.From.ID
		text = update.Message.Text
		// Alias telegram-safe commands to agy commands
		if strings.HasPrefix(text, "/grill_me") {
			text = strings.Replace(text, "/grill_me", "/grill-me", 1)
		} else if strings.HasPrefix(text, "/teamwork_preview") {
			text = strings.Replace(text, "/teamwork_preview", "/teamwork-preview", 1)
		}
		caption = update.Message.Caption

		if update.Message.Document != nil {
			fileID = update.Message.Document.FileID
			ext = filepath.Ext(update.Message.Document.FileName)
			if ext == "" {
				ext = ".bin"
			}
		} else if len(update.Message.Photo) > 0 {
			fileID = update.Message.Photo[len(update.Message.Photo)-1].FileID
			ext = ".jpg"
		} else if update.Message.Voice != nil {
			fileID = update.Message.Voice.FileID
			ext = ".ogg"
		} else if update.Message.Audio != nil {
			fileID = update.Message.Audio.FileID
			ext = ".mp3"
		}
	} else if update.CallbackQuery != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
		userID = update.CallbackQuery.From.ID
		text = update.CallbackQuery.Data
	}

	user := getUser(db, userID)
	botName := bot.Self.UserName

	// Handle Callbacks
	if update.CallbackQuery != nil {
		data := update.CallbackQuery.Data
		if strings.HasPrefix(data, "ans:") {
			text = strings.TrimPrefix(data, "ans:")
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		}
		if strings.HasPrefix(data, "resume:") {
			convID := strings.TrimPrefix(data, "resume:")
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))

			sessionMu.Lock()
			if old, ok := globalSessions[botName]; ok {
				if old.Cmd != nil && old.Cmd.Process != nil {
					old.Cmd.Process.Kill()
				}
				delete(globalSessions, botName)
			}
			sessionMu.Unlock()

			updateUserSession(db, userID, convID)

			session := &AgySession{
				BotName:      botName,
				Model:        user.Model,
				Workspace:    user.Workspace,
				Conversation: convID,
			}
			session.start()
			sessionMu.Lock()
			globalSessions[botName] = session
			sessionMu.Unlock()

			// Read the init event to confirm
			if session.StdoutScanner.Scan() {
				var initData map[string]interface{}
				if json.Unmarshal([]byte(session.StdoutScanner.Text()), &initData) == nil {
					session.Conversation, _ = initData["conversation_id"].(string)
				}
			}

			respText := fmt.Sprintf("🔄 Resumed session!\n`%s`", convID[:8])
			msg := tgbotapi.NewMessage(chatID, respText)
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
		}
		var respText string
		var markup *tgbotapi.InlineKeyboardMarkup

		if strings.HasPrefix(data, "model:") {
			newModel := strings.TrimPrefix(data, "model:")
			updateUserModel(db, userID, newModel)
			
			sessionMu.Lock()
			if old, ok := globalSessions[botName]; ok {
				if old.Cmd != nil && old.Cmd.Process != nil {
					old.Cmd.Process.Kill()
				}
				delete(globalSessions, botName)
			}
			sessionMu.Unlock()
			
			respText = "✅ Model changed to `" + newModel + "`"
		} else if data == "cmd:status" {
			respText = fmt.Sprintf("📊 *Status:*\n\n*Bot:* `%s`\n*Workspace:* `%s`\n*Model:* `%s`\n*Session:* `%s`", botName, user.Workspace, user.Model, user.SessionID)
		} else if data == "cmd:model" {
			respText = "🧠 Select a model:"
			// Create keyboard
			m := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("⚡ 3.7 Flash High", "model:gemini-3.7-flash-high"),
					tgbotapi.NewInlineKeyboardButtonData("⚡ 3.7 Flash Med", "model:gemini-3.7-flash-medium"),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("⚡ 3.6 Flash High", "model:gemini-3.6-flash-high"),
					tgbotapi.NewInlineKeyboardButtonData("⚡ 3.6 Flash Low", "model:gemini-3.6-flash-low"),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🧠 3.1 Pro High", "model:gemini-3.1-pro-high"),
				),
			)
			markup = &m
		} else if data == "cmd:clear" {
			sessionMu.Lock()
			if old, ok := globalSessions[botName]; ok {
				if old.Cmd != nil && old.Cmd.Process != nil {
					old.Cmd.Process.Kill()
				}
				delete(globalSessions, botName)
			}
			sessionMu.Unlock()
			
			newUUID := uuid.New().String()
			updateUserSession(db, userID, newUUID)
			respText = "🧼 Context cleared!\n`" + newUUID + "`"
		} else if data == "cmd:usage" {
			text = "/usage"
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		} else if data == "cmd:resume" {
			text = "/resume"
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		} else if data == "cmd:rename" {
			text = "/rename"
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		} else if data == "cmd:help" {
			text = "/help"
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		}

		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		if markup != nil {
			msg.ReplyMarkup = markup
		}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
		return
	}

ProcessInput:
	downloadedFile := false
	if fileID != "" {
		fileURL, err := bot.GetFileDirectURL(fileID)
		if err == nil {
			downloadDir := fmt.Sprintf("/root/.agents/%s/scratch/downloads", botName)
			os.MkdirAll(downloadDir, 0755)
			safePath := filepath.Join(downloadDir, uuid.New().String()+ext)
			
			bot.Send(tgbotapi.NewMessage(chatID, "📥 Downloading file..."))
			
			resp, err := http.Get(fileURL)
			if err == nil {
				defer resp.Body.Close()
				out, _ := os.Create(safePath)
				io.Copy(out, resp.Body)
				out.Close()
				
				baseText := text
				if baseText == "" {
					baseText = caption
				}
				text = fmt.Sprintf("[Attached File: file://%s]\n\n%s", safePath, baseText)
				downloadedFile = true
			}
		}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	if text == "/start" || text == fmt.Sprintf("/start@%s", botName) {
		sessionTitle := "(пусто)"
		stepsCount := 0
		uptimeStr := "0 м"
		
		brainDir := "/root/.gemini/antigravity-cli/brain"
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
					uptimeStr = fmt.Sprintf("%d ч %d м", int(dur.Hours()), int(dur.Minutes())%60)
				} else {
					uptimeStr = fmt.Sprintf("%d м", int(dur.Minutes()))
				}
			}
			if sessionTitle == "(пусто)" && stepsCount > 0 {
				sessionTitle = "Сессия активна"
			}
		}

		respText := fmt.Sprintf(`🛰 *Терминал Агента*
🤖 *Агент:* @%s
🟢 *Статус:* Ожидание задачи

📂 *CWD:* `+"`%s`"+`
🧠 *Модель:* `+"`%s`"+`

📋 *Сессия:* %s
⏱ *Аптайм:* %s
👣 *Шагов:* %d`, botName, user.Workspace, user.Model, sessionTitle, uptimeStr, stepsCount)

		m := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🧠 Модель", "cmd:model"),
				tgbotapi.NewInlineKeyboardButtonData("📊 Квота", "cmd:usage"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🧼 Очистить", "cmd:clear"),
				tgbotapi.NewInlineKeyboardButtonData("🔄 Сессии", "cmd:resume"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✏️ Переименовать", "cmd:rename"),
				tgbotapi.NewInlineKeyboardButtonData("🆘 Help", "cmd:help"),
			),
		)
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = m
		bot.Send(msg)
		return
	} else if text == "/resume" || text == fmt.Sprintf("/resume@%s", botName) {
		// List recent conversations from brain directory
		brainDir := "/root/.gemini/antigravity-cli/brain"
		entries, err := os.ReadDir(brainDir)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to read sessions"))
			return
		}

		type convInfo struct {
			ID      string
			ModTime time.Time
			Title   string
		}
		var convs []convInfo

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			transcript := filepath.Join(brainDir, e.Name(), ".system_generated", "logs", "transcript.jsonl")
			if _, err := os.Stat(transcript); err != nil {
				continue
			}
			info, _ := e.Info()
			f, err := os.Open(transcript)
			if err != nil {
				continue
			}
			title := ""
			titleFile := filepath.Join(brainDir, e.Name(), ".title")
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
					// Extract text between <USER_REQUEST> tags
					start := strings.Index(content, "<USER_REQUEST>")
					end := strings.Index(content, "</USER_REQUEST>")
					if start >= 0 && end > start {
						inner := strings.TrimSpace(content[start+14 : end])
						// Strip nested tags
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
			convs = append(convs, convInfo{ID: e.Name(), ModTime: info.ModTime(), Title: title})
		}

		// Sort by modification time descending
		for i := 0; i < len(convs); i++ {
			for j := i + 1; j < len(convs); j++ {
				if convs[j].ModTime.After(convs[i].ModTime) {
					convs[i], convs[j] = convs[j], convs[i]
				}
			}
		}

		// Take top 8
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
			if len(cbData) > 64 {
				cbData = cbData[:64]
			}
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, cbData),
			))
		}
		m := tgbotapi.NewInlineKeyboardMarkup(rows...)
		msg := tgbotapi.NewMessage(chatID, "🔄 *Select a session to resume:*")
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = m
		bot.Send(msg)
		return
	} else if strings.HasPrefix(text, "/workspace") {
		parts := strings.SplitN(text, " ", 2)
		if len(parts) < 2 {
			msg := tgbotapi.NewMessage(chatID, "⚠️ Usage: `/workspace <absolute path>`\n📂 Current workspace: `" + user.Workspace + "`")
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
		}
		newWS := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(newWS, "/") {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Error: path must be absolute (e.g. `/root/projects/app`)"))
			return
		}
		
		_, err := db.Exec("UPDATE users SET workspace = ? WHERE user_id = ?", newWS, userID)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: " + err.Error()))
			return
		}
		
		sessionMu.Lock()
		if old, ok := globalSessions[botName]; ok {
			if old.Cmd != nil && old.Cmd.Process != nil {
				old.Cmd.Process.Kill()
			}
			delete(globalSessions, botName)
		}
		sessionMu.Unlock()
		
		msg := tgbotapi.NewMessage(chatID, "📂 Target Lab (Workspace) changed to: `" + newWS + "`\nSession restarted with new mount!")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	} else if strings.HasPrefix(text, "/rename") {
		parts := strings.SplitN(text, " ", 2)
		if len(parts) < 2 {
			msg := tgbotapi.NewMessage(chatID, "⚠️ Usage: `/rename <new name>`")
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
		}
		newName := strings.TrimSpace(parts[1])
		
		session := getSession(botName, user)
		if session.Conversation != "" {
			sessionDir := filepath.Join("/root/.gemini/antigravity-cli/brain", session.Conversation)
			os.MkdirAll(sessionDir, 0755)
			titleFile := filepath.Join(sessionDir, ".title")
			err := os.WriteFile(titleFile, []byte(newName), 0644)
			if err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to save name: " + err.Error()))
			} else {
				bot.Send(tgbotapi.NewMessage(chatID, "✅ Session renamed to: *" + newName + "*"))
			}
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ No active session to rename."))
		}
		return
	} else if text == "/usage" || text == fmt.Sprintf("/usage@%s", botName) {
		cmd := exec.Command("/tmp/agy_wrapper.sh", "--print", "/usage")
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
		return
	} else if text == "/help" || text == fmt.Sprintf("/help@%s", botName) {
		respText := "🆘 *Справка по командам:*\n\n" +
			"• /start - Показать дашборд\n" +
			"• /model - Изменить модель LLM\n" +
			"• /usage - Просмотр квоты\n" +
			"• /clear - Очистить контекст (сбросить сессию)\n" +
			"• /resume - Вернуться к предыдущей сессии\n" +
			"• /rename <имя> - Переименовать текущую сессию\n" +
			"• /workspace <путь> - Сменить рабочую папку\n\n" +
			"*Отправь любой текст или файл, чтобы Агент начал работу.*"
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	} else if text == "/model" || text == fmt.Sprintf("/model@%s", botName) {
		respText := "🧠 Select a model:"
		m := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.7 Flash High", "model:gemini-3.7-flash-high")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.7 Flash Med", "model:gemini-3.7-flash-medium")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.6 Flash High", "model:gemini-3.6-flash-high")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.6 Flash Low", "model:gemini-3.6-flash-low")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.5 Flash High", "model:gemini-3.5-flash-high")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 3.5 Flash Low", "model:gemini-3.5-flash-low")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🧠 Gemini 3.1 Pro High", "model:gemini-3.1-pro-high")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🧠 Gemini 3.1 Pro Low", "model:gemini-3.1-pro-low")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🟣 Claude Sonnet 4.6", "model:claude-sonnet-4-6")),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🟣 Claude Opus 4.6", "model:claude-opus-4-6-thinking")),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🟢 GPT-OSS 120B", "model:gpt-oss-120b-medium"),
			),
		)
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = m
		bot.Send(msg)
		return
	} else if text == "/clear" || text == fmt.Sprintf("/clear@%s", botName) {
		sessionMu.Lock()
		if old, ok := globalSessions[botName]; ok {
			if old.Cmd != nil && old.Cmd.Process != nil {
				old.Cmd.Process.Kill()
			}
			delete(globalSessions, botName)
		}
		sessionMu.Unlock()
		
		newUUID := uuid.New().String()
		updateUserSession(db, userID, newUUID)
		respText := "🧼 Context cleared!\n`" + newUUID + "`"
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	session := getSession(botName, user)
	session.mu.Lock()
	defer session.mu.Unlock()
	session.BotAPI = bot
	session.ChatID = chatID

	var activeMessageID int
	if !downloadedFile {
		msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
		msg.ParseMode = "Markdown"
		sentMsg, err := bot.Send(msg)
		if err == nil {
			activeMessageID = sentMsg.MessageID
			session.ActiveMessageID = activeMessageID
		}
	} else {
		// Assuming the "Downloading file..." was the last message, we can't easily edit it without its ID,
		// so we just send a new message.
		msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
		msg.ParseMode = "Markdown"
		sentMsg, err := bot.Send(msg)
		if err == nil {
			activeMessageID = sentMsg.MessageID
			session.ActiveMessageID = activeMessageID
		}
	}
	
	if session.Cmd == nil || session.Cmd.Process == nil {
	    session.start()
	}

	payload := map[string]interface{}{
		"event": "user",
		"message": map[string]string{
			"content": text,
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	payloadBytes = append(payloadBytes, '\n')
	_, err := session.Stdin.Write(payloadBytes)
	if err != nil {
		session.Restart()
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Agent process crashed. Send your message again."))
		return
	}
}

func sendChunk(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string) []string {
	log.Printf("sendChunk called for chatID %d, msgID %d, text len %d", chatID, messageID, len(text))
	chunks := SplitHTMLChunks(MarkdownToTelegramHTML(text), 4000)
	chunkToEdit := chunks[0]
	if len(chunks) > 1 && strings.Contains(text, "⏳") {
		chunkToEdit += "\n\n<i>[Truncated while typing...]</i>"
	}
	
	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, chunkToEdit)
	editMsg.ParseMode = "HTML"
	_, err := bot.Send(editMsg)
	if err != nil && err.Error() != "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message" {
		log.Printf("Edit error: %v", err)
	}
	return chunks
}

func startBotPolling(botToken string, allowedAdmins map[int64]bool, wg *sync.WaitGroup) {
	defer wg.Done()
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Printf("Failed to init bot: %v", err)
		return
	}
	
	registerBotCommands(bot)
	db := initDB(bot.Self.UserName)
	log.Printf("[Bot %s] Started in PURE GO mode", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		var userID int64
		if update.Message != nil {
			userID = update.Message.From.ID
		} else if update.CallbackQuery != nil {
			userID = update.CallbackQuery.From.ID
		}

		if userID != 0 && !allowedAdmins[userID] {
			log.Printf("[Bot %s] 🛑 ACL BLOCK: Unauthorized access attempt from %d", bot.Self.UserName, userID)
			continue
		}

		go handleUpdate(bot, update, db)
	}
}


func registerBotCommands(bot *tgbotapi.BotAPI) {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Welcome menu & status"},
		{Command: "model", Description: "Select LLM model"},
		{Command: "usage", Description: "Show API quota usage"},
		{Command: "clear", Description: "Clear context and restart agent"},
		{Command: "resume", Description: "Resume previous conversation"},
		{Command: "rename", Description: "Rename current session"},
		{Command: "workspace", Description: "Change target workspace directory"},
		{Command: "goal", Description: "Run exhaustive long-running task"},
		{Command: "schedule", Description: "Set recurring schedule or timer"},
		{Command: "browser", Description: "Use web browser for a task"},
		{Command: "plan", Description: "Step-by-step task planning"},
		{Command: "grill_me", Description: "Interactive design interview"},
		{Command: "teamwork_preview", Description: "Swarm autonomous agents"},
		{Command: "learn", Description: "Persist behavior for future tasks"},
	}
	cfg := tgbotapi.NewSetMyCommands(commands...)
	if _, err := bot.Request(cfg); err != nil {
		log.Printf("Failed to set bot commands: %v", err)
	} else {
		log.Printf("Successfully registered slash commands for %s", bot.Self.UserName)
	}
}

func main() {
	tokensEnv := os.Getenv("BOT_TOKENS")
	if tokensEnv == "" {
		log.Fatal("BOT_TOKENS env var required")
	}
	
	allowedAdmins := loadAllowedAdmins()
	var wg sync.WaitGroup

	for _, t := range strings.Split(tokensEnv, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			wg.Add(1)
			go startBotPolling(t, allowedAdmins, &wg)
		}
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs

	log.Println("Shutting down...")
	// We don't bother cleanly closing all bots here, systemd handles it.
}

func (s *AgySession) readStdoutLoop() {
	for s.StdoutScanner.Scan() {
		line := s.StdoutScanner.Text()
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		event, _ := data["event"].(string)
		if event == "init" {
			newID, _ := data["conversation_id"].(string)
			if newID != "" && newID != s.Conversation {
			    s.Conversation = newID
			    // Update DB
			    dbPath := fmt.Sprintf("sessions_%s.db", s.BotName)
			    if localDB, err := sql.Open("sqlite3", dbPath); err == nil {
			        localDB.Exec("UPDATE users SET session_id = ? WHERE chat_id = ?", newID, s.ChatID)
			        localDB.Close()
			    }
			}
		} else if event == "step_update" {
			su, ok := data["step_update"].(map[string]interface{})
			if ok {
				if tcs, ok := su["tool_calls"].([]interface{}); ok && len(tcs) > 0 {
					for _, tcRaw := range tcs {
						tc, ok := tcRaw.(map[string]interface{})
						if !ok { continue }
						if name, _ := tc["name"].(string); name == "ask_question" {
							argJSON, _ := tc["argumentsJson"].(string)
							var args map[string]interface{}
							json.Unmarshal([]byte(argJSON), &args)
							
							questions, _ := args["questions"].([]interface{})
							if len(questions) > 0 {
								qMap, _ := questions[0].(map[string]interface{})
								qText, _ := qMap["question"].(string)
								opts, _ := qMap["options"].([]interface{})
								
								if len(opts) > 0 {
									var rows [][]tgbotapi.InlineKeyboardButton
									for _, optRaw := range opts {
										optStr := fmt.Sprintf("%v", optRaw)
										callbackData := "ans:" + optStr
										if len(callbackData) > 64 { callbackData = callbackData[:64] }
										row := tgbotapi.NewInlineKeyboardRow(
											tgbotapi.NewInlineKeyboardButtonData(optStr, callbackData),
										)
										rows = append(rows, row)
									}
									m := tgbotapi.NewInlineKeyboardMarkup(rows...)
									
									msg := tgbotapi.NewMessage(s.ChatID, "❓ *Question from Agent:*\n" + qText)
									msg.ParseMode = "Markdown"
									msg.ReplyMarkup = m
									if s.BotAPI != nil {
										s.BotAPI.Send(msg)
									}
								}
							}
						}
					}
				}

				if delta, ok := su["text_delta"].(string); ok && delta != "" {
					s.TextBuffer += delta
				}
			}
		} else if event == "result" {
			res, ok := data["result"].(map[string]interface{})
			if ok {
				if status, _ := res["status"].(string); status == "ERROR" {
					errMsg, _ := res["error"].(string)
					if s.ActiveMessageID == 0 {
					    msg := tgbotapi.NewMessage(s.ChatID, "❌ Error from agent: "+errMsg)
					    s.BotAPI.Send(msg)
					} else {
					    sendChunk(s.BotAPI, s.ChatID, s.ActiveMessageID, "❌ Error from agent: "+errMsg)
					}
					s.ActiveMessageID = 0
					s.TextBuffer = ""
					continue
				}
				response := s.TextBuffer
				if response == "" {
					response = "No response from agent."
				}
				if s.ActiveMessageID == 0 {
						chunks := SplitHTMLChunks(MarkdownToTelegramHTML(response), 4000)
						for _, chunk := range chunks {
					    	msg := tgbotapi.NewMessage(s.ChatID, chunk)
							msg.ParseMode = "HTML"
					    	s.BotAPI.Send(msg)
						}
					} else {
						chunks := sendChunk(s.BotAPI, s.ChatID, s.ActiveMessageID, response)
						if len(chunks) > 1 {
							for i := 1; i < len(chunks); i++ {
								msg := tgbotapi.NewMessage(s.ChatID, chunks[i])
								msg.ParseMode = "HTML"
								s.BotAPI.Send(msg)
							}
						}
					}
				sendArtifacts(s.BotAPI, s.ChatID, response)
				s.ActiveMessageID = 0
				s.TextBuffer = ""
			}
		}
	}
}
