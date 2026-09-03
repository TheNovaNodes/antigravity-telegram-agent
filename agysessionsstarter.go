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
	VoiceReply   bool
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
	startMu         sync.Mutex
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

// initDB initializes the SQLite database for a specific bot, enables WAL mode, and creates necessary tables.
func initDB(botName string) *sql.DB {
	dbPath := fmt.Sprintf("sessions_%s.db", botName)
	// Ensure secure permissions (0600) on database file to prevent unauthorized local reading
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600); err == nil {
		f.Close()
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db %s: %v", dbPath, err)
	}

	// Performance & Concurrency Hardening: Enable WAL mode and 5s busy timeout
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA busy_timeout=5000;")
	db.Exec("PRAGMA synchronous=NORMAL;")

	_, err = db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '',
		model TEXT DEFAULT '%s',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL,
		voice_reply BOOLEAN DEFAULT 0
	)`, defaultModel))
	if err != nil {
		log.Fatal(err)
	}

	// Dynamic migration for existing users table
	db.Exec("ALTER TABLE users ADD COLUMN voice_reply BOOLEAN DEFAULT 0")

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
	var voiceReplyVal int
	if db != nil {
		err := db.QueryRow("SELECT user_id, workspace, model, is_first_start, session_id, COALESCE(voice_reply, 0) FROM users WHERE user_id = ?", userID).Scan(
			&u.ID, &u.Workspace, &u.Model, &u.IsFirstStart, &u.SessionID, &voiceReplyVal)
		if err == nil {
			u.VoiceReply = (voiceReplyVal == 1)
			return u
		}
		if err == sql.ErrNoRows {
			u = User{
				ID:           userID,
				Workspace:    filepath.Join(getAgentsDir(), botName),
				Model:        defaultModel,
				IsFirstStart: true,
				SessionID:    uuid.New().String(),
				VoiceReply:   false,
			}
			_, err = db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id, voice_reply) VALUES (?, ?, ?, ?, ?, 0)",
				u.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)
			if err != nil {
				log.Printf("DB Error inserting user %d: %v", u.ID, err)
			}
			return u
		}
	}
	if u.ID == 0 {
		u = User{
			ID:           userID,
			Workspace:    filepath.Join(getAgentsDir(), botName),
			Model:        defaultModel,
			IsFirstStart: true,
			SessionID:    uuid.New().String(),
			VoiceReply:   false,
		}
	}
	return u
}

// updateUserSession updates the active conversation session ID for a specific user.
func updateUserSession(db *sql.DB, userID int64, sessionID string) {
	if db == nil {
		return
	}
	_, err := db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)
	if err != nil {
		log.Printf("DB Error updating session for user %d: %v", userID, err)
	}
	_, err = db.Exec("INSERT OR IGNORE INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
	if err != nil {
		log.Printf("DB Error inserting session_history for user %d: %v", userID, err)
	}
}

// updateUserVoiceReply toggles the persistent voice response mode for a user.
func updateUserVoiceReply(db *sql.DB, userID int64, enabled bool) {
	if db == nil {
		return
	}
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.Exec("UPDATE users SET voice_reply = ? WHERE user_id = ?", val, userID)
	if err != nil {
		log.Printf("DB Error updating voice_reply for user %d: %v", userID, err)
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

// getAgentsDir resolves the base directory for all agent workspaces.
func getAgentsDir() string {
	if env := os.Getenv("AGENTS_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".agents")
}

// getBrainDir resolves the Antigravity CLI brain storage directory.
func getBrainDir() string {
	if env := os.Getenv("BRAIN_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".gemini/antigravity-cli/brain")
}

// getAgyPath resolves the absolute path to the Antigravity CLI binary.
func getAgyPath() string {
	if env := os.Getenv("AGY_BINARY"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".local/bin/agy")
}

// Kill gracefully cancels the session context, closes pipes, and terminates the underlying process tree.
func (s *AgySession) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isAlive = false
	if s.cancel != nil {
		s.cancel()
	}
	if s.Stdin != nil {
		s.Stdin.Close()
		s.Stdin = nil
	}
	if s.Cmd != nil && s.Cmd.Process != nil {
		pid := s.Cmd.Process.Pid
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			s.Cmd.Process.Kill()
		}
	}
}

// GetConversation safely returns the active conversation ID under mutex lock.
func (s *AgySession) GetConversation() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Conversation
}

// SetConversation safely updates the active conversation ID under mutex lock.
func (s *AgySession) SetConversation(convID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Conversation = convID
}

// sendTypingAction sends a ChatTyping or ChatRecordVoice action to Telegram if the session is actively generating a response.
func (s *AgySession) sendTypingAction() {
	s.mu.Lock()
	botAPI := s.BotAPI
	chatID := s.ChatID
	activeMsgID := s.ActiveMessageID
	isVoice := s.VoiceReply
	s.mu.Unlock()

	if botAPI == nil || chatID == 0 || activeMsgID == 0 {
		return
	}

	action := tgbotapi.ChatTyping
	if isVoice {
		action = tgbotapi.ChatRecordVoice
	}
	botAPI.Send(tgbotapi.NewChatAction(chatID, action))
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

	session := &AgySession{
		BotName:      botName,
		ChatID:       chatID,
		UserID:       user.ID,
		DB:           db,
		Model:        newModel,
		Workspace:    newWorkspace,
		Conversation: convID,
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
		VoiceReply:   user.VoiceReply,
	}

	globalSessions[sessionKey] = session
	sessionMu.Unlock()

	session.start()

	return session
}

// getSession retrieves an active session for the user or creates a new isolated agent process.
func getSession(botName string, user User, chatID int64) *AgySession {
	sessionKey := fmt.Sprintf("%s:%d:%d", botName, chatID, user.ID)

	sessionMu.Lock()
	session, exists := globalSessions[sessionKey]
	if exists && session.Model == user.Model && session.Workspace == user.Workspace && session.GetConversation() == user.SessionID {
		if session.IsAlive() {
			sessionMu.Unlock()
			return session
		}
	}

	if exists {
		session.Kill()
		delete(globalSessions, sessionKey)
	}

	session = &AgySession{
		BotName:      botName,
		ChatID:       chatID,
		UserID:       user.ID,
		DB:           nil,
		Model:        user.Model,
		Workspace:    user.Workspace,
		Conversation: user.SessionID,
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
		VoiceReply:   user.VoiceReply,
	}

	globalSessions[sessionKey] = session
	sessionMu.Unlock()

	session.start()

	return session
}

// start initializes the Antigravity CLI process, sets up pipes, and starts the asynchronous throttler loop.
func (s *AgySession) start() {
	s.startMu.Lock()
	defer s.startMu.Unlock()

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

	agyPath := getAgyPath()
	cmd := exec.Command(agyPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set the actual OS-level CWD (Personal Office) for the agent
	agentDir := filepath.Join(getAgentsDir(), s.BotName)
	os.MkdirAll(agentDir, 0755)
	cmd.Dir = agentDir

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var scanner *bufio.Scanner
	if stdout != nil {
		scanner = bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 10*1024*1024) // 10MB max token size
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start agy process for %s (%s): %v", s.BotName, agyPath, err)
		s.mu.Lock()
		s.Cmd = nil
		s.Stdin = nil
		s.StdoutScanner = nil
		s.isAlive = false
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.Cmd = cmd
	s.Stdin = stdin
	s.StdoutScanner = scanner
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

	// Periodic typing / record voice action loop
	go func(ctx context.Context) {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sendTypingAction()
			}
		}
	}(ctx)

	go s.readStdoutLoop(scanner, ctx)
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
	allowedRootBrain, _ := filepath.Abs(getBrainDir())

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

// handleStartCommand renders the status dashboard with session metrics and quick action keyboard.
func handleStartCommand(bot *tgbotapi.BotAPI, chatID int64, botName string, user User) {
	sessionTitle := "(empty)"
	stepsCount := 0
	uptimeStr := "0 m"

	brainDir := getBrainDir()
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
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			continue
		}
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

	if stepNum == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "📭 Transcript is currently empty."))
		return
	}

	exportDir := filepath.Join(getAgentsDir(), botName, "scratch", "exports")
	os.MkdirAll(exportDir, 0755)
	safeFilename := fmt.Sprintf("session_%s.md", user.SessionID[:8])
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

// handleVoiceToggleCommand enables or disables persistent voice replies for the user.
func handleVoiceToggleCommand(bot *tgbotapi.BotAPI, chatID, userID int64, text string, user User, db *sql.DB) {
	arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "/voice")))
	newState := !user.VoiceReply
	if arg == "on" || arg == "1" || arg == "true" {
		newState = true
	} else if arg == "off" || arg == "0" || arg == "false" {
		newState = false
	}

	updateUserVoiceReply(db, userID, newState)
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
	allowedRoot, _ := filepath.Abs(getAgentsDir())
	if !strings.HasPrefix(realPath, allowedRoot) {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Path must be under "+allowedRoot))
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

	newUUID := uuid.New().String()
	updateUserSession(db, userID, newUUID)
	replaceSession(db, botName, user, newUUID, user.Model, newWS, chatID)

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

	session := getSession(botName, user, chatID)
	conv := session.GetConversation()
	if conv != "" {
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
	newUUID := uuid.New().String()
	replaceSession(db, botName, user, newUUID, user.Model, user.Workspace, chatID)
	updateUserSession(db, userID, newUUID)
	respText := "🧼 Context cleared!\n`" + newUUID + "`"
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

// handleCommand parses and routes slash commands. Returns true if handled.
func handleCommand(bot *tgbotapi.BotAPI, chatID, userID int64, text, botName string, user User, db *sql.DB) bool {
	if text == "/start" || text == fmt.Sprintf("/start@%s", botName) {
		handleStartCommand(bot, chatID, botName, user)
		return true
	}
	if text == "/resume" || text == fmt.Sprintf("/resume@%s", botName) {
		handleResumeCommand(bot, chatID, userID, db)
		return true
	}
	if strings.HasPrefix(text, "/tts") {
		handleTTSCommand(bot, chatID, text)
		return true
	}
	if strings.HasPrefix(text, "/voice") {
		handleVoiceToggleCommand(bot, chatID, userID, text, user, db)
		return true
	}
	if strings.HasPrefix(text, "/workspace") {
		handleWorkspaceCommand(bot, chatID, userID, text, botName, user, db)
		return true
	}
	if strings.HasPrefix(text, "/rename") {
		handleRenameCommand(bot, chatID, text, botName, user)
		return true
	}
	if text == "/export" || text == fmt.Sprintf("/export@%s", botName) {
		handleExportCommand(bot, chatID, userID, botName, user)
		return true
	}
	if text == "/usage" || text == fmt.Sprintf("/usage@%s", botName) {
		handleUsageCommand(bot, chatID)
		return true
	}
	if text == "/help" || text == fmt.Sprintf("/help@%s", botName) {
		handleHelpCommand(bot, chatID)
		return true
	}
	if text == "/model" || text == fmt.Sprintf("/model@%s", botName) {
		handleModelCommand(bot, chatID)
		return true
	}
	if text == "/refresh_models" || text == fmt.Sprintf("/refresh_models@%s", botName) {
		handleRefreshModelsCommand(bot, chatID)
		return true
	}
	if text == "/clear" || text == fmt.Sprintf("/clear@%s", botName) {
		handleClearCommand(bot, chatID, userID, botName, user, db)
		return true
	}
	return false
}

// handleCallbackQuery processes all inline keyboard callback events.
func handleCallbackQuery(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, user User, botName string, db *sql.DB) {
	chatID := cb.Message.Chat.ID
	userID := cb.From.ID
	data := cb.Data

	bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	if strings.HasPrefix(data, "ans:") {
		ansText := strings.TrimPrefix(data, "ans:")
		handleMessagePayload(bot, chatID, userID, ansText, botName, user, false, false, db)
		return
	}

	if strings.HasPrefix(data, "resume:") {
		convID := strings.TrimPrefix(data, "resume:")
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
		handleModelCommand(bot, chatID)
		return
	} else if data == "cmd:clear" {
		newUUID := uuid.New().String()
		replaceSession(db, botName, user, newUUID, user.Model, user.Workspace, chatID)
		updateUserSession(db, userID, newUUID)
		respText = "🧼 Context cleared!\n`" + newUUID + "`"
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
	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file."))
		return "", false, err
	}
	downloadDir := filepath.Join(getAgentsDir(), botName, "scratch", "downloads")
	os.MkdirAll(downloadDir, 0755)
	safePath := filepath.Join(downloadDir, uuid.New().String()+ext)

	bot.Send(tgbotapi.NewMessage(chatID, "📥 Downloading file..."))

	resp, err := http.Get(fileURL)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download file."))
		return "", false, err
	}
	defer resp.Body.Close()

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
	if isVoice || user.VoiceReply {
		session.VoiceReply = true
	}
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

		action := tgbotapi.ChatTyping
		if session.VoiceReply {
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

	var err error
	if session.Stdin != nil {
		_, err = session.Stdin.Write(payloadBytes)
	} else {
		err = fmt.Errorf("agent stdin pipe is not available")
	}
	session.mu.Unlock()

	if err != nil {
		session.Restart()
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Agent process was not ready. Send your message again."))
		return
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
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERED in startBotPolling] %v", r)
		}
	}()
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
		{Command: "export", Description: "Export session transcript to file"},
		{Command: "voice", Description: "Toggle persistent voice mode"},
		{Command: "tts", Description: "Text to speech voice synthesis"},
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
	agyPath := getAgyPath()
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
		s.Kill()
	}
	sessionMu.Unlock()

	// Give children time to flush
	time.Sleep(2 * time.Second)
	log.Println("Goodbye.")
}

// readStdoutLoop asynchronously reads JSONL output from the agent's stdout and processes events like text deltas and errors.
func (s *AgySession) readStdoutLoop(params ...interface{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERED in readStdoutLoop for bot %s] %v", s.BotName, r)
		}
	}()

	var scanner *bufio.Scanner
	var ctx context.Context

	if len(params) >= 2 {
		if sc, ok := params[0].(*bufio.Scanner); ok {
			scanner = sc
		}
		if c, ok := params[1].(context.Context); ok {
			ctx = c
		}
	}

	if scanner == nil || ctx == nil {
		s.mu.Lock()
		if scanner == nil {
			scanner = s.StdoutScanner
		}
		if ctx == nil {
			ctx = s.ctx
		}
		s.mu.Unlock()
	}

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
	go func(sc *bufio.Scanner, c context.Context) {
		defer close(lines)
		for sc.Scan() {
			select {
			case <-c.Done():
				return
			case lines <- sc.Text():
			}
		}
	}(scanner, ctx)

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
				s.Conversation = newID
				uID := s.UserID
				db := s.DB
				s.mu.Unlock()
				if diff && db != nil && uID != 0 {
					db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", newID, uID)
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
				botAPI := s.BotAPI
				targetChatID := s.ChatID
				s.mu.Unlock()

				if shouldVoice && response != "" && botAPI != nil {
					go func(b *tgbotapi.BotAPI, cID int64, txt string) {
						err := GenerateAndSendVoice(b, cID, txt)
						if err != nil {
							msg := tgbotapi.NewMessage(cID, "❌ TTS Error: "+err.Error())
							b.Send(msg)
						}
					}(botAPI, targetChatID, response)
				}
			}
		}
	}
}
