package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// AgySession represents the active execution session of an Antigravity agent process.
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

var globalSessions = make(map[string]*AgySession)
var sessionMu sync.Mutex

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

// Restart gracefully restarts the session.
func (s *AgySession) Restart() {
	s.Kill()
	s.start()
}

// ExtractAllowedArtifacts parses the agent's markdown text for local file links (file://),
// normalizes the paths, checks them against the LFI whitelists, and verifies files exist.
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

// sendArtifacts parses the agent's response and sends valid files as Telegram documents.
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

// readStdoutLoop asynchronously reads JSONL output from the agent's stdout and processes events.
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
