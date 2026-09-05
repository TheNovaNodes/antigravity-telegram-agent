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

const maxTextBufferBytes = 1 << 20 // 1MB maximum streaming buffer per session

// AgySession represents the active execution session of an Antigravity agent process.
type AgySession struct {
	BotName         string
	Model           string
	Workspace       string
	Conversation    string
	UseContinue     bool
	Cmd             *exec.Cmd
	Stdin           io.WriteCloser
	StdoutPipe      io.ReadCloser
	StdoutScanner   *bufio.Scanner
	mu              sync.Mutex
	startMu         sync.Mutex
	BotAPI          *tgbotapi.BotAPI
	ChatID          int64
	UserID          int64
	DB              *sql.DB
	ActiveMessageID int
	TextBuffer      string
	TextTruncated   bool
	LastEdit        time.Time
	LastActivity    time.Time
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

// getProjectsDir resolves the directory for project workspaces.
func getProjectsDir() string {
	if env := os.Getenv("PROJECTS_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, "projects")
}

// isPathUnderRoot checks whether a given path is located strictly within the root directory (Fail-Closed).
func isPathUnderRoot(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil || absPath == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil || absRoot == "" {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

var fallbackAgyBinary string

// getAgyPath resolves the absolute path to the Antigravity CLI binary.
func getAgyPath() string {
	if env := os.Getenv("AGY_BINARY"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	p := filepath.Join(home, ".local/bin/agy")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if fallbackAgyBinary != "" {
		return fallbackAgyBinary
	}
	return p
}

// Kill gracefully cancels the session context, closes pipes, and terminates the underlying process tree.
// It extracts process handles under mutex lock and performs blocking I/O and OS syscalls outside the lock
// to eliminate thread contention and prevent potential deadlocks.
func (s *AgySession) Kill() {
	s.mu.Lock()
	s.isAlive = false
	cancel := s.cancel
	s.cancel = nil
	stdin := s.Stdin
	s.Stdin = nil
	stdout := s.StdoutPipe
	s.StdoutPipe = nil
	cmd := s.Cmd
	s.Cmd = nil
	s.ActiveMessageID = 0
	s.TextBuffer = ""
	s.TextTruncated = false
	s.StdoutScanner = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stdin != nil {
		stdin.Close()
	}
	if stdout != nil {
		stdout.Close()
	}
	if cmd != nil && cmd.Process != nil {
		pid := cmd.Process.Pid
		// Two-phase graceful termination: SIGTERM then SIGKILL
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			for i := 0; i < 5; i++ {
				time.Sleep(50 * time.Millisecond)
				if err := syscall.Kill(-pid, 0); err != nil {
					// Process has terminated
					close(done)
					return
				}
			}
			// Force SIGKILL if still running
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			cmd.Process.Kill()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
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

// SessionOptions encapsulates parameters for acquiring or provisioning an agent session.
type SessionOptions struct {
	DB           *sql.DB
	BotName      string
	User         User
	ChatID       int64
	ConvID       string
	ExplicitConv bool
	Model        string
	Workspace    string
	ForceRestart bool
	UseContinue  bool
}

// acquireSession retrieves an active matching session or provisions and starts a new isolated agent process.
// If ForceRestart is true or if environment parameters differ, any prior session is terminated.
func acquireSession(opts SessionOptions) (*AgySession, error) {
	model := opts.Model
	if model == "" {
		model = opts.User.Model
	}
	workspace := opts.Workspace
	if workspace == "" {
		workspace = opts.User.Workspace
	}
	convID := opts.ConvID
	if !opts.ExplicitConv && convID == "" {
		convID = opts.User.SessionID
	}

	sessionKey := fmt.Sprintf("%s:%d:%d", opts.BotName, opts.ChatID, opts.User.ID)

	sessionMu.Lock()
	session, exists := globalSessions[sessionKey]
	if !opts.ForceRestart && exists && session.Model == model && session.Workspace == workspace {
		activeConv := session.GetConversation()
		convMatches := (activeConv == convID) || (convID == "") || (activeConv == "") ||
			(isValidSessionID(activeConv) && !isValidSessionID(convID))

		if convMatches && session.IsAlive() {
			session.mu.Lock()
			if session.DB == nil && opts.DB != nil {
				session.DB = opts.DB
			}
			session.LastActivity = time.Now()
			session.mu.Unlock()
			sessionMu.Unlock()
			return session, nil
		}
	}

	if exists {
		session.Kill()
		delete(globalSessions, sessionKey)
	}

	newSession := &AgySession{
		BotName:      opts.BotName,
		ChatID:       opts.ChatID,
		UserID:       opts.User.ID,
		DB:           opts.DB,
		Model:        model,
		Workspace:    workspace,
		Conversation: convID,
		UseContinue:  opts.UseContinue,
		InitChan:     make(chan string, 1),
		UpdateChan:   make(chan struct{}, 100),
		VoiceReply:   opts.User.VoiceReply,
		LastActivity: time.Now(),
	}

	globalSessions[sessionKey] = newSession
	sessionMu.Unlock()

	if err := newSession.start(); err != nil {
		sessionMu.Lock()
		if globalSessions[sessionKey] == newSession {
			delete(globalSessions, sessionKey)
		}
		sessionMu.Unlock()
		return newSession, err
	}

	return newSession, nil
}

// replaceSession handles the graceful termination of an existing agent session
// and provisions a new isolated agent process with updated environment parameters.
func replaceSession(db *sql.DB, botName string, user User, convID string, newModel string, newWorkspace string, chatID int64) (*AgySession, error) {
	return acquireSession(SessionOptions{
		DB:           db,
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ConvID:       convID,
		ExplicitConv: true,
		Model:        newModel,
		Workspace:    newWorkspace,
		ForceRestart: true,
	})
}

// getSession retrieves an active session for the user or creates a new isolated agent process.
func getSession(botName string, user User, chatID int64, dbs ...*sql.DB) *AgySession {
	var db *sql.DB
	if len(dbs) > 0 {
		db = dbs[0]
	}
	session, _ := acquireSession(SessionOptions{
		DB:           db,
		BotName:      botName,
		User:         user,
		ChatID:       chatID,
		ConvID:       user.SessionID,
		Model:        user.Model,
		Workspace:    user.Workspace,
		ForceRestart: false,
	})
	return session
}

// CleanIdleSessions scans globalSessions and terminates processes idle longer than maxIdleDuration.
func CleanIdleSessions(maxIdleDuration time.Duration) int {
	sessionMu.Lock()
	now := time.Now()
	var toEvict []*AgySession
	var toEvictKeys []string

	for k, s := range globalSessions {
		s.mu.Lock()
		lastAct := s.LastActivity
		if lastAct.IsZero() {
			lastAct = s.LastEdit
		}
		s.mu.Unlock()

		if !lastAct.IsZero() && now.Sub(lastAct) > maxIdleDuration {
			toEvict = append(toEvict, s)
			toEvictKeys = append(toEvictKeys, k)
		}
	}

	for _, k := range toEvictKeys {
		delete(globalSessions, k)
	}
	sessionMu.Unlock()

	for _, s := range toEvict {
		s.Kill()
		log.Printf("[GC] Evicted idle session for bot %s (chatID %d)", s.BotName, s.ChatID)
	}
	return len(toEvict)
}

// StartSessionGCWorker runs a background timer to periodically evict idle sessions.
func StartSessionGCWorker(interval, maxIdle time.Duration, stopChan <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				CleanIdleSessions(maxIdle)
			}
		}
	}()
}

// start initializes the Antigravity CLI process, sets up pipes, and starts the asynchronous throttler loop.
func (s *AgySession) start() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	s.mu.Lock()
	if s.isAlive {
		s.mu.Unlock()
		return nil
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
	cmd.WaitDelay = 2 * time.Second

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
		s.StdoutPipe = nil
		s.StdoutScanner = nil
		s.isAlive = false
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.Cmd = cmd
	s.Stdin = stdin
	s.StdoutPipe = stdout
	s.StdoutScanner = scanner
	s.isAlive = true
	s.mu.Unlock()

	// Streaming throttler loop
	go func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVERED in session throttler for bot %s] %v", s.BotName, r)
			}
		}()
		ticker := time.NewTicker(1200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.UpdateChan:
				s.mu.Lock()
				truncated := s.TextTruncated
				text := s.TextBuffer
				activeMsgID := s.ActiveMessageID
				botAPI := s.BotAPI
				chatID := s.ChatID
				s.mu.Unlock()

				if truncated {
					text += "\n\n⚠️ <i>[Response truncated: buffer exceeded 1MB limit]</i>"
				}

				if activeMsgID != 0 && botAPI != nil {
					sendChunk(botAPI, chatID, activeMsgID, text)
				}
			case <-ticker.C:
				s.mu.Lock()
				truncated := s.TextTruncated
				text := s.TextBuffer
				activeMsgID := s.ActiveMessageID
				botAPI := s.BotAPI
				chatID := s.ChatID
				s.mu.Unlock()

				if truncated {
					text += "\n\n⚠️ <i>[Response truncated: buffer exceeded 1MB limit]</i>"
				}

				if activeMsgID != 0 && botAPI != nil {
					sendChunk(botAPI, chatID, activeMsgID, text)
				}
			}
		}
	}(ctx)

	// Typing indicator loop
	go func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVERED in session typing indicator for bot %s] %v", s.BotName, r)
			}
		}()
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
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVERED in session cmdWait for bot %s] %v", s.BotName, r)
			}
		}()
		err := c.Wait()
		s.mu.Lock()
		activeMsgID := s.ActiveMessageID
		botAPI := s.BotAPI
		chatID := s.ChatID
		if s.Cmd == c || s.Cmd == nil {
			s.isAlive = false
			s.Cmd = nil
			s.Stdin = nil
			s.StdoutPipe = nil
			s.StdoutScanner = nil
			s.ActiveMessageID = 0
			s.TextBuffer = ""
			s.TextTruncated = false
		}
		s.mu.Unlock()

		// If the process exited unexpectedly while a message was active, clean up the Telegram UI spinner
		if activeMsgID != 0 && botAPI != nil {
			statusMsg := "⚠️ *Сессия агента была остановлена или перезапущена.* Пожалуйста, отправьте сообщение повторно."
			if err != nil {
				log.Printf("[Process exited for bot %s] %v", s.BotName, err)
			}
			sendChunk(botAPI, chatID, activeMsgID, statusMsg)
		}
	}(cmd)

	return nil
}

// Restart gracefully restarts the session.
func (s *AgySession) Restart() {
	s.Kill()
	_ = s.start()
}

// ExtractAllowedArtifacts parses the agent's markdown text for local file links (file://),
// normalizes the paths, evaluates symlinks, checks them against the LFI whitelists (Fail-Closed), and verifies files exist.
func ExtractAllowedArtifacts(text string) []string {
	var validPaths []string
	re := regexp.MustCompile(`\(file://(.*?)\)`)
	matches := re.FindAllStringSubmatch(text, -1)

	agentsDir := getAgentsDir()
	brainDir := getBrainDir()
	projectsDir := getProjectsDir()

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

			// Fail-Closed: Verify that realPath is strictly contained inside allowed roots
			isAllowed := isPathUnderRoot(realPath, agentsDir) ||
				isPathUnderRoot(realPath, brainDir) ||
				isPathUnderRoot(realPath, projectsDir)

			if !isAllowed {
				log.Printf("ExtractAllowedArtifacts: blocked attempt to send file outside allowed root: %s", realPath)
				continue
			}

			if info, err := os.Stat(realPath); err == nil && !info.IsDir() {
				validPaths = append(validPaths, realPath)
			}
		}
	}
	return validPaths
}

// sendArtifacts parses the agent's response, opens verified files to prevent TOCTOU, and sends them as Telegram documents.
func sendArtifacts(bot *tgbotapi.BotAPI, chatID int64, text string) {
	paths := ExtractAllowedArtifacts(text)
	for _, realPath := range paths {
		f, err := os.Open(realPath)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			f.Close()
			continue
		}

		doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
			Name:   filepath.Base(realPath),
			Reader: f,
		})
		doc.Caption = "📦 Artifact: " + filepath.Base(realPath)
		if bot != nil {
			bot.Send(doc)
		}
		f.Close()
	}
}

// fallbackChain defines the prioritized failover progression for LLM models upon encountering rate limits (429/503).
var fallbackChain = []string{
	"gemini-3.8-flash-high",
	"gemini-3.7-flash-high",
	"gemini-3.1-pro-high",
	"gemini-3.6-flash-low",
}

// getFallbackModel provides an automatic failover model when rate limits or quota exhaustion are encountered.
// It iterates through fallbackChain. If currentModel is found, it returns the next model in sequence.
// If currentModel is not in the chain, it fails over to the head of the chain (unless already equal).
// It returns an empty string when the fallback chain is exhausted, preventing infinite switching loops.
func getFallbackModel(currentModel string) string {
	for i, m := range fallbackChain {
		if m == currentModel {
			if i+1 < len(fallbackChain) {
				return fallbackChain[i+1]
			}
			return "" // Chain exhausted, terminal state
		}
	}
	// If currentModel is not in fallbackChain, fail over to the first entry (flagship)
	if len(fallbackChain) > 0 && fallbackChain[0] != currentModel {
		return fallbackChain[0]
	}
	return ""
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
		s.TextBuffer = ""
		s.mu.Unlock()
	}()

	lines := make(chan string, 1000)
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
				s.Conversation = newID
				uID := s.UserID
				db := s.DB
				s.mu.Unlock()
				if db != nil && uID != 0 {
					updateUserSession(db, uID, newID)
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
										callbackData := storeQuestionOption(optStr)
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
					if len(s.TextBuffer)+len(delta) <= maxTextBufferBytes {
						s.TextBuffer += delta
					} else {
						s.TextTruncated = true
					}
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

					errLower := strings.ToLower(errMsg)
					isRateLimit := strings.Contains(errMsg, "429") ||
						strings.Contains(errMsg, "503") ||
						strings.Contains(errLower, "timeout") ||
						strings.Contains(errLower, "rate limit") ||
						strings.Contains(errLower, "quota") ||
						strings.Contains(errLower, "resource_exhausted")

					displayErr := "❌ Error from agent: " + errMsg
					if isRateLimit {
						fallback := getFallbackModel(s.Model)
						if fallback != "" && fallback != s.Model {
							oldModel := s.Model
							s.mu.Lock()
							s.Model = fallback
							uID := s.UserID
							db := s.DB
							s.mu.Unlock()

							if db != nil && uID != 0 {
								updateUserModel(db, uID, fallback)
							}

							displayErr = fmt.Sprintf("⚠️ Достигнут лимит для `%s` (Quota/429).\n🔄 *Авто-переключение на `%s`*.\nОтправьте сообщение повторно для продолжения.", oldModel, fallback)
						} else {
							displayErr = "⚠️ Превышен лимит запросов ко всем доступным моделям (Rate limit / Quota Exhausted). Пожалуйста, подождите некоторое время и отправьте сообщение повторно."
						}
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
					s.TextTruncated = false
					s.mu.Unlock()
					continue
				}
				s.mu.Lock()
				response := s.TextBuffer
				if s.TextTruncated {
					response += "\n\n⚠️ <i>[Response truncated: buffer exceeded 1MB limit]</i>"
				}
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
