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
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
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
	BotName         string
	Model           string
	Workspace       string
	Conversation    string
	UseContinue     bool
	Cmd             *exec.Cmd
	Stdin           io.WriteCloser
	StdoutScanner   *bufio.Scanner
	mu              sync.Mutex
	BotAPI          *tgbotapi.BotAPI
	ChatID          int64
	UserID          int64
	DB              *sql.DB
	ActiveMessageID int
	TextBuffer      string
	LastEdit        time.Time
	UpdateChan      chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	InitChan        chan string
	VoiceReply      bool
	isAlive         bool
}
const defaultModel = "gemini-3.8-flash-high"

// initDB initializes the SQLite database for a specific bot and creates necessary tables.
func initDB(botName string) *sql.DB {
	dbPath := fmt.Sprintf("sessions_%s.db", botName)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db %s: %v", dbPath, err)
	}
	_, err = db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '',
		model TEXT DEFAULT '%s',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL
	)`, defaultModel))
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS session_history (
		user_id INTEGER,
		session_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, session_id)
	)`)
	if err != nil {
		log.Fatal(err)
	}
	return db
}

// getUser retrieves a user from the database or creates a new entry with default settings if not found.
// getUser queries the database for an existing user configuration.
// If the user does not exist, it initializes a new record with default
// settings (workspace, model, session UUID) and returns the User struct.
func getUser(db *sql.DB, userID int64, botName string) User {
	var u User
	err := db.QueryRow("SELECT user_id, workspace, model, is_first_start, session_id FROM users WHERE user_id = ?", userID).Scan(
		&u.ID, &u.Workspace, &u.Model, &u.IsFirstStart, &u.SessionID)
	if err == sql.ErrNoRows {
		u = User{
			ID:           userID,
			Workspace:    filepath.Join(getAgentsDir(), botName),
			Model:        defaultModel,
			IsFirstStart: true,
			SessionID:    uuid.New().String(),
		}
		_, err = db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",
			u.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)
		if err != nil {
			log.Printf("DB Error inserting user %d: %v", u.ID, err)
		}
	} else if u.SessionID == "" {
		u.SessionID = uuid.New().String()
		_, err = db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", u.SessionID, u.ID)
		if err != nil {
			log.Printf("DB Error updating session_id for user %d: %v", u.ID, err)
		}
	}
	return u
}

// updateUserSession updates the active conversation session ID for a specific user.
func updateUserSession(db *sql.DB, userID int64, sessionID string) {
	_, err := db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)
	if err != nil {
		log.Printf("DB Error updating session for user %d: %v", userID, err)
	}
	_, err = db.Exec("INSERT OR IGNORE INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
	if err != nil {
		log.Printf("DB Error inserting session_history for user %d: %v", userID, err)
	}
}

// updateUserModel updates the selected LLM model for a specific user.
func updateUserModel(db *sql.DB, userID int64, model string) error {
	_, err := db.Exec("UPDATE users SET model = ? WHERE user_id = ?", model, userID)
	if err != nil {
		log.Printf("DB Error updating model for user %d: %v", userID, err)
	}
	return err
}

// loadAllowedAdmins parses the ALLOWED_ADMIN_IDS environment variable into a map for quick lookups.
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
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err == nil && id != 0 {
			allowed[id] = true
		}
	}
	return allowed
}

var globalSessions = make(map[string]*AgySession)
var sessionMu sync.Mutex

// getAgentsDir resolves the base directory for all agent workspaces, defaulting to the user's home directory.
func getAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/root/.agents"
	}
	return filepath.Join(home, ".agents")
}

// Kill gracefully cancels the session context and terminates the underlying process.
func (s *AgySession) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isAlive = false
	if s.cancel != nil {
		s.cancel()
	}
	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
	}
}

// IsAlive checks whether the underlying agent process is currently running.
func (s *AgySession) IsAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isAlive
}

// replaceSession handles the graceful termination of an existing agent session 
// and provisions a new isolated agent process with updated environment parameters.
// It ensures there are no goroutine or memory leaks from the previous context.
func replaceSession(db *sql.DB, botName string, user User, convID string, newModel string, newWorkspace string, chatID int64) *AgySession {
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)
	
	sessionMu.Lock()
	if old, ok := globalSessions[sessionKey]; ok {
		old.Kill()
		delete(globalSessions, sessionKey)
	}
	sessionMu.Unlock()

	session := &AgySession{
		BotName:      botName,
		Model:        newModel,
		Workspace:    newWorkspace,
		Conversation: convID,
		UserID:       user.ID,
		DB:           db,
		UpdateChan:   make(chan struct{}, 1),
		InitChan:     make(chan string, 1),
	}

	session.start()
	
	sessionMu.Lock()
	globalSessions[sessionKey] = session
	sessionMu.Unlock()
	
	return session
}

// getSession retrieves an active session for the user or creates a new isolated agent process.
func getSession(botName string, user User, chatID int64) *AgySession {
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)
	
	sessionMu.Lock()
	session, exists := globalSessions[sessionKey]
	if exists && session.Model == user.Model && session.Workspace == user.Workspace && session.Conversation == user.SessionID {
		if session.IsAlive() {
			sessionMu.Unlock()
			return session
		}
	}

	if exists {
		session.Kill()
		delete(globalSessions, sessionKey)
	}
	sessionMu.Unlock()

	session = &AgySession{
		BotName:      botName,
		Model:        user.Model,
		Workspace:    user.Workspace,
		Conversation: user.SessionID,
		UserID:       user.ID,
		UpdateChan:   make(chan struct{}, 1),
		InitChan:     make(chan string, 1),
	}

	session.start()
	
	sessionMu.Lock()
	globalSessions[sessionKey] = session
	sessionMu.Unlock()
	
	return session
}

// start initializes the Antigravity CLI process, sets up pipes, and starts the asynchronous throttler loop.
func (s *AgySession) start() {
	s.mu.Lock()
	if s.isAlive {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	s.mu.Unlock()

	args := []string{
		"--model", s.Model,
		"--dangerously-skip-permissions",
		"--mode", "accept-edits",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "1h",
		"--add-dir", filepath.Join(getAgentsDir(), "common"),
		"--add-dir", filepath.Join(getAgentsDir(), s.BotName),
		"--add-dir", s.Workspace,
	}
	if s.UseContinue {
		args = append(args, "--continue")
	} else if s.Conversation != "" {
		args = append(args, "--conversation", s.Conversation)
	}

	agyPath := os.Getenv("AGY_BINARY")
	if agyPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/root"
		}
		agyPath = filepath.Join(home, ".local/bin/agy")
	}
	cmd := exec.Command(agyPath, args...)

	// Set the actual OS-level CWD (Personal Office) for the agent
	agentDir := filepath.Join(getAgentsDir(), s.BotName)
	os.MkdirAll(agentDir, 0755)
	cmd.Dir = agentDir

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	s.mu.Lock()
	s.Cmd = cmd
	s.Stdin = stdin
	if stdout != nil {
		s.StdoutScanner = bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		s.StdoutScanner.Buffer(buf, 10*1024*1024) // 10MB max token size
	}
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start agy process for %s (%s): %v", s.BotName, agyPath, err)
		s.mu.Lock()
		s.Cmd = nil
		s.isAlive = false
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.isAlive = true
	s.mu.Unlock()

	// Streaming throttler loop
	go func(ctx context.Context) {
		var lastSent string
		var lastSentTime time.Time
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.UpdateChan:
			case <-ticker.C:
			case <-ctx.Done():
				return
			}

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
		}
	}(ctx)

	go s.readStdoutLoop()
	go func(c *exec.Cmd) {
		c.Wait()
		s.mu.Lock()
		s.isAlive = false
		if s.Cmd == c {
			s.Cmd = nil
		}
		s.ActiveMessageID = 0
		s.TextBuffer = ""
		s.mu.Unlock()
	}(cmd)
}

// Restart performs the Restart method.
func (s *AgySession) Restart() {
	s.Kill()
	s.start()
}

// ExtractAllowedArtifacts parses the agent's markdown text for local file links (file://),
// normalizes the paths, checks them against the LFI (Local File Inclusion) whitelists
// (the agents directory and the brain directory), and verifies the files exist on disk.
// Returns a slice of valid, safe, and existing absolute file paths.
func ExtractAllowedArtifacts(text string) []string {
	var validPaths []string
	re := regexp.MustCompile(`\(file://(.*?)\)`)
	matches := re.FindAllStringSubmatch(text, -1)
	
	allowedRootAgents, _ := filepath.Abs(getAgentsDir())
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	allowedRootBrain, _ := filepath.Abs(filepath.Join(home, ".gemini/antigravity-cli/brain"))

	for _, match := range matches {
		if len(match) > 1 {
			filePath := match[1]
			if decoded, err := url.PathUnescape(filePath); err == nil {
				filePath = decoded
			}
			cleanPath, err := filepath.Abs(filePath)
			if err != nil {
				continue
			}
			
			realPath, err := filepath.EvalSymlinks(cleanPath)
			if err != nil {
				continue
			}
			
			if !strings.HasPrefix(realPath, allowedRootAgents) && !strings.HasPrefix(realPath, allowedRootBrain) {
				log.Printf("ExtractAllowedArtifacts: blocked attempt to send file outside allowed root: %s", realPath)
				continue
			}
			
			if _, err := os.Stat(realPath); err == nil {
				validPaths = append(validPaths, realPath)
			}
		}
	}
	return validPaths
}

// sendArtifacts parses the agent's response using ExtractAllowedArtifacts and sends the
// resulting valid files to the Telegram chat as document attachments.
func sendArtifacts(bot *tgbotapi.BotAPI, chatID int64, text string) {
	paths := ExtractAllowedArtifacts(text)
	for _, cleanPath := range paths {
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(cleanPath))
		doc.Caption = "📦 Artifact: " + filepath.Base(cleanPath)
		if bot != nil {
			bot.Send(doc)
		}
	}
}

// handleUpdate is the primary router for incoming Telegram messages and inline callbacks.
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

		if text == "" && caption == "" && fileID == "" {
			bot.Request(tgbotapi.NewMessage(chatID, "⚠️ Sticker/contact/location не поддерживается. Отправьте текст, фото, документ или голосовое."))
			return
		}
	} else if update.CallbackQuery != nil {
		chatID = update.CallbackQuery.Message.Chat.ID
		userID = update.CallbackQuery.From.ID
		text = update.CallbackQuery.Data
	}

	botName := bot.Self.UserName
	user := getUser(db, userID, botName)
	_ = fmt.Sprintf("%s:%d", botName, userID)

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

			updateUserSession(db, userID, convID)
			session := replaceSession(db, botName, user, convID, user.Model, user.Workspace, chatID)

			select {
			case newID := <-session.InitChan:
				session.Conversation = newID
				convID = newID
			case <-time.After(3 * time.Second):
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
			if err := updateUserModel(db, userID, newModel); err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: "+err.Error()))
				return
			}

			user.Model = newModel
			newUUID := uuid.New().String()
			replaceSession(db, botName, user, newUUID, newModel, user.Workspace, chatID)
			updateUserSession(db, userID, newUUID)

			respText = "✅ Model changed to `" + newModel + "`\n\n⚠️ *Warning:* Agent restarted. Background tasks were stopped."
		} else if data == "cmd:status" {
			respText = fmt.Sprintf("📊 *Status:*\n\n*Bot:* `%s`\n*Workspace:* `%s`\n*Model:* `%s`\n*Session:* `%s`", botName, user.Workspace, user.Model, user.SessionID)
		} else if data == "cmd:model" {
			text = "/model"
			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			goto ProcessInput
		} else if data == "cmd:clear" {
			

			newUUID := uuid.New().String()
			replaceSession(db, botName, user, newUUID, user.Model, user.Workspace, chatID)
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
			downloadDir := filepath.Join(getAgentsDir(), botName, "scratch", "downloads")
			os.MkdirAll(downloadDir, 0755)
			safePath := filepath.Join(downloadDir, uuid.New().String()+ext)

			bot.Send(tgbotapi.NewMessage(chatID, "📥 Downloading file..."))

			resp, err := http.Get(fileURL)
			if err == nil {
				out, err := os.Create(safePath)
				if err != nil {
					resp.Body.Close()
					bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to save file to disk."))
					return
				}
				
				const maxUploadBytes = 100 << 20 // 100 MB
				if resp.ContentLength > maxUploadBytes {
					out.Close()
					resp.Body.Close()
					os.Remove(safePath)
					bot.Send(tgbotapi.NewMessage(chatID, "❌ File too large (max 100 MB)"))
					return
				}
				
				n, err := io.Copy(out, io.LimitReader(resp.Body, maxUploadBytes+1))
				out.Close()
				resp.Body.Close()
				
				if n > maxUploadBytes {
					os.Remove(safePath)
					bot.Send(tgbotapi.NewMessage(chatID, "❌ File exceeds 100 MB limit"))
					return
				}

				baseText := text
				if baseText == "" {
					baseText = caption
				}
				text = fmt.Sprintf("[Attached File: file://%s]\n\n%s", safePath, baseText)
				downloadedFile = true
			} else {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file."))
				return
			}
		}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	if text == "/start" || text == fmt.Sprintf("/start@%s", botName) {
		sessionTitle := "(empty)"
		stepsCount := 0
		uptimeStr := "0 m"

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
					uptimeStr = fmt.Sprintf("%d h %d m", int(dur.Hours()), int(dur.Minutes())%60)
				} else {
					uptimeStr = fmt.Sprintf("%d m", int(dur.Minutes()))
				}
			}
			if sessionTitle == "(empty)" && stepsCount > 0 {
				sessionTitle = "Session active"
			}
		}

		respText := fmt.Sprintf(`🛰 *Agent Terminal*
🤖 *Agent:* `+"`@%s`"+`
🟢 *Status:* Awaiting task

📂 *CWD:* `+"`%s`"+`
🧠 *Model:* `+"`%s`"+`

📋 *Session:* %s
⏱ *Uptime:* %s
👣 *Steps:* %d`, botName, user.Workspace, user.Model, sessionTitle, uptimeStr, stepsCount)

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
				tgbotapi.NewInlineKeyboardButtonData("🆘 Help", "cmd:help"),
			),
		)
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = m
		bot.Send(msg)
		return
	} else if text == "/resume" || text == fmt.Sprintf("/resume@%s", botName) {
		// List recent conversations from user history
		brainDir := "/root/.gemini/antigravity-cli/brain"
		dbRows, err := db.Query("SELECT session_id FROM session_history WHERE user_id = ? ORDER BY created_at DESC LIMIT 20", userID)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to read session history"))
			return
		}
		defer dbRows.Close()

		type convInfo struct {
			ID      string
			ModTime time.Time
			Title   string
		}
		var convs []convInfo
		var validSessionIDs []string

		for dbRows.Next() {
			var sid string
			if err := dbRows.Scan(&sid); err == nil {
				validSessionIDs = append(validSessionIDs, sid)
			}
		}

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
			convs = append(convs, convInfo{ID: sid, ModTime: stat.ModTime(), Title: title})
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
	} else if strings.HasPrefix(text, "/tts ") {
		ttsText := strings.TrimPrefix(text, "/tts ")
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
		
		// Delete the generating message
		bot.Send(tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID))
		
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ TTS Error: "+err.Error()))
		}
		return
	} else if strings.HasPrefix(text, "/workspace") {
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
		allowedRoot, _ := filepath.Abs(getAgentsDir())
		if !strings.HasPrefix(realPath, allowedRoot) {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Path must be under " + allowedRoot))
			return
		}
		newWS = realPath

		_, err := db.Exec("UPDATE users SET workspace = ? WHERE user_id = ?", newWS, userID)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: "+err.Error()))
			return
		}

		newUUID := uuid.New().String()
		updateUserSession(db, userID, newUUID)
		replaceSession(db, botName, user, newUUID, user.Model, newWS, chatID)

		msg := tgbotapi.NewMessage(chatID, "📂 Target Lab (Workspace) changed to: `"+newWS+"`\n\n⚠️ *Warning:* Session restarted. All active background tasks and subagents were terminated.")
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

		session := getSession(botName, user, chatID)
		if session.Conversation != "" {
			sessionDir := filepath.Join("/root/.gemini/antigravity-cli/brain", session.Conversation)
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
		return
	} else if text == "/usage" || text == fmt.Sprintf("/usage@%s", botName) {
				agyPath := os.Getenv("AGY_BINARY")
		if agyPath == "" {
			agyPath = "/root/.local/bin/agy"
		}
		cmd := exec.Command(agyPath, "--print", "/usage")
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
		respText := "🆘 *Command Reference:*\n\n" +
			"• /start - Show dashboard\n" +
			"• /model - Change LLM model\n" +
			"• /usage - Check API quota\n" +
			"• /clear - Clear context (reset session)\n" +
			"• /resume - Resume previous session\n" +
			"• /rename <name> - Rename current session\n" +
			"• /workspace <path> - Change working directory\n\n" +
			"*Send any text or file to start the Agent.*"
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	} else if text == "/model" || text == fmt.Sprintf("/model@%s", botName) {
		respText := "🧠 Select a model:"
		modelsMu.RLock()
		cachedModels := make([]AgyModel, len(availableModels))
		copy(cachedModels, availableModels)
		modelsMu.RUnlock()

		var rows [][]tgbotapi.InlineKeyboardButton
		if len(cachedModels) == 0 {
			// Fallback if cache is empty
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
		return
	} else if text == "/refresh_models" || text == fmt.Sprintf("/refresh_models@%s", botName) {
		fetchModels()
		modelsMu.RLock()
		count := len(availableModels)
		modelsMu.RUnlock()
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Dynamically fetched %d models from agy.", count)))
		return
	} else if text == "/clear" || text == fmt.Sprintf("/clear@%s", botName) {
		newUUID := uuid.New().String()
		replaceSession(db, botName, user, newUUID, user.Model, user.Workspace, chatID)
		updateUserSession(db, userID, newUUID)
		respText := "🧼 Context cleared!\n`" + newUUID + "`"
		msg := tgbotapi.NewMessage(chatID, respText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	session := getSession(botName, user, chatID)
	
	if update.Message != nil && update.Message.Voice != nil {
		session.mu.Lock()
		session.VoiceReply = true
		session.mu.Unlock()
	}

	session.mu.Lock()
	session.BotAPI = bot
	session.ChatID = chatID

	if !session.isAlive {
		session.ActiveMessageID = 0
		session.TextBuffer = ""
		session.mu.Unlock()
		session.start()
		session.mu.Lock()
	}

	if !downloadedFile {
		activeMsgID := session.ActiveMessageID
		if activeMsgID == 0 {
			msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
			msg.ParseMode = "Markdown"
			sentMsg, err := bot.Send(msg)
			if err == nil {
				session.ActiveMessageID = sentMsg.MessageID
			}
		} else {
			msg := tgbotapi.NewMessage(chatID, "⏳ _Message queued..._")
			msg.ParseMode = "Markdown"
			bot.Send(msg)
		}
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
	
	session.mu.Unlock()
	
	if err != nil {
		session.Restart()
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Agent process crashed. Send your message again."))
		return
	}
}

// sendChunk safely breaks a large text into valid HTML chunks and sends them sequentially to respect Telegram limits.
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

// startBotPolling initializes a Telegram Bot instance and starts its dedicated long-polling loop.
func startBotPolling(botToken string, allowedAdmins map[int64]bool, wg *sync.WaitGroup) {
	defer wg.Done()
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Printf("Failed to init bot: %v", err)
		return
	}
	bot.Client = &http.Client{Timeout: 65 * time.Second}

	registerBotCommands(bot)
	db := initDB(bot.Self.UserName)
	defer db.Close()
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

// registerBotCommands sets the default slash commands menu for the Telegram bot interface.
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

type AgyModel struct {
	ID    string
	Name  string
	Emoji string
}

var (
	availableModels []AgyModel
	modelsMu        sync.RWMutex
)

func getEmojiForModel(id string) string {
	id = strings.ToLower(id)
	if strings.Contains(id, "flash") {
		return "⚡"
	}
	if strings.Contains(id, "pro") {
		return "🧠"
	}
	if strings.Contains(id, "claude") {
		return "🟣"
	}
	if strings.Contains(id, "oss") || strings.Contains(id, "llama") {
		return "🟢"
	}
	return "🤖"
}

func fetchModels() {
	agyPath := os.Getenv("AGY_BINARY")
	if agyPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/root"
		}
		agyPath = filepath.Join(home, ".local/bin/agy")
	}
	cmd := exec.Command(agyPath, "models")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to fetch dynamic models: %v", err)
		return
	}

	var parsed []AgyModel
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			id := parts[0]
			name := strings.Join(parts[1:], " ")
			parsed = append(parsed, AgyModel{
				ID:    id,
				Name:  name,
				Emoji: getEmojiForModel(id),
			})
		}
	}

	if len(parsed) > 0 {
		modelsMu.Lock()
		availableModels = parsed
		modelsMu.Unlock()
		log.Printf("Dynamically loaded %d models", len(parsed))
	}
}

// main is the entry point that spins up multiple bot instances concurrently based on the BOT_TOKENS environment variable.
func main() {
	fetchModels()
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

	log.Println("Shutting down gracefully...")
	sessionMu.Lock()
	for _, s := range globalSessions {
		if s.cancel != nil {
			s.cancel()
		}
		if s.Cmd != nil && s.Cmd.Process != nil {
			log.Printf("Killing child PID %d for user session", s.Cmd.Process.Pid)
			s.Cmd.Process.Kill()
		}
	}
	sessionMu.Unlock()
	
	// Give children time to flush
	time.Sleep(2 * time.Second)
	log.Println("Goodbye.")
}

// readStdoutLoop asynchronously reads JSONL output from the agent's stdout and processes events like text deltas and errors.
func (s *AgySession) readStdoutLoop() {
	s.mu.Lock()
	scanner := s.StdoutScanner
	ctx := s.ctx
	s.mu.Unlock()
	if scanner == nil || ctx == nil {
		return
	}

	defer func() {
		s.mu.Lock()
		s.ActiveMessageID = 0
		s.TextBuffer = ""
		s.mu.Unlock()
	}()

	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			case lines <- scanner.Text():
			}
		}
	}()

	for {
		var line string
		select {
		case <-ctx.Done():
			return
		case l, ok := <-lines:
			if !ok {
				return
			}
			line = l
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			continue
		}

		event, _ := data["event"].(string)
		if event == "init" {
			newID, _ := data["conversation_id"].(string)
			if newID != "" {
				select {
				case s.InitChan <- newID:
				default:
				}
				s.mu.Lock()
				diff := (newID != s.Conversation)
				if diff {
					s.Conversation = newID
				}
				s.mu.Unlock()
				if diff && s.DB != nil {
					s.DB.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", newID, s.UserID)
				}
			}
		} else if event == "step_update" {
			su, ok := data["step_update"].(map[string]interface{})
			if ok {
				if tcs, ok := su["tool_calls"].([]interface{}); ok && len(tcs) > 0 {
					for _, tcRaw := range tcs {
						tc, ok := tcRaw.(map[string]interface{})
						if !ok {
							continue
						}
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
										if len(callbackData) > 64 {
											callbackData = callbackData[:64]
										}
										row := tgbotapi.NewInlineKeyboardRow(
											tgbotapi.NewInlineKeyboardButtonData(optStr, callbackData),
										)
										rows = append(rows, row)
									}
									m := tgbotapi.NewInlineKeyboardMarkup(rows...)

									msg := tgbotapi.NewMessage(s.ChatID, "❓ *Question from Agent:*\n"+qText)
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
					s.mu.Lock()
					s.TextBuffer += delta
					s.mu.Unlock()
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}
				}
			}
		} else if event == "result" {
			res, ok := data["result"].(map[string]interface{})
			if ok {
				if status, _ := res["status"].(string); status == "ERROR" {
					errMsg, _ := res["error"].(string)
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}
					s.mu.Lock()
					activeMsgID := s.ActiveMessageID
					s.mu.Unlock()

					isRateLimit := strings.Contains(errMsg, "429") || strings.Contains(errMsg, "503") || strings.Contains(strings.ToLower(errMsg), "timeout") || strings.Contains(strings.ToLower(errMsg), "rate limit")

					displayErr := "❌ Error from agent: " + errMsg
					if isRateLimit {
						displayErr = "⚠️ Превышен лимит запросов к модели (Rate limit / 429). Пожалуйста, подождите некоторое время и отправьте сообщение повторно."
					}

					if s.BotAPI != nil {
						if activeMsgID == 0 {
							msg := tgbotapi.NewMessage(s.ChatID, displayErr)
							s.BotAPI.Send(msg)
						} else {
							sendChunk(s.BotAPI, s.ChatID, activeMsgID, displayErr)
						}
					}

					s.Kill()
					s.mu.Lock()
					s.ActiveMessageID = 0
					s.TextBuffer = ""
					s.mu.Unlock()
					continue
				}
				s.mu.Lock()
				response := s.TextBuffer
				activeMsgID := s.ActiveMessageID
				s.mu.Unlock()
				
				if response == "" {
					response = "No response from agent."
				}
				if s.BotAPI != nil {
					if activeMsgID == 0 {
						chunks := SplitHTMLChunks(MarkdownToTelegramHTML(response), 4000)
						for _, chunk := range chunks {
							msg := tgbotapi.NewMessage(s.ChatID, chunk)
							msg.ParseMode = "HTML"
							s.BotAPI.Send(msg)
						}
					} else {
						chunks := sendChunk(s.BotAPI, s.ChatID, activeMsgID, response)
						if len(chunks) > 1 {
							for i := 1; i < len(chunks); i++ {
								msg := tgbotapi.NewMessage(s.ChatID, chunks[i])
								msg.ParseMode = "HTML"
								s.BotAPI.Send(msg)
							}
						}
					}
					sendArtifacts(s.BotAPI, s.ChatID, response)
				}
				
				// Mirror Protocol: Trigger TTS on final response
				s.mu.Lock()
				shouldVoice := s.VoiceReply
				s.VoiceReply = false
				s.ActiveMessageID = 0
				s.TextBuffer = ""
				s.mu.Unlock()
				
				if shouldVoice && response != "" {
					go func(chatID int64, txt string) {
						err := GenerateAndSendVoice(s.BotAPI, chatID, txt)
						if err != nil {
							msg := tgbotapi.NewMessage(chatID, "❌ TTS Error: " + err.Error())
							if s.BotAPI != nil {
								s.BotAPI.Send(msg)
							}
						}
					}(s.ChatID, response)
				}
			}
		}
	}
}
