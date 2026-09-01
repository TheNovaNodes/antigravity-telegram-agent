import re

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# Fix 20: DB Error handling
text = text.replace(
    'db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",\\n\\t\\t\\tu.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)',
    '_, err = db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id) VALUES (?, ?, ?, ?, ?)",\\n\\t\\t\\tu.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)\\n\\t\\tif err != nil {\\n\\t\\t\\tlog.Printf("DB Error inserting user %d: %v", u.ID, err)\\n\\t\\t}'
)
text = text.replace(
    'db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", u.SessionID, u.ID)',
    '_, err = db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", u.SessionID, u.ID)\\n\\t\\tif err != nil {\\n\\t\\t\\tlog.Printf("DB Error updating session_id for user %d: %v", u.ID, err)\\n\\t\\t}'
)
text = text.replace(
    'func updateUserSession(db *sql.DB, userID int64, sessionID string) {\\n\\tdb.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)\\n}',
    'func updateUserSession(db *sql.DB, userID int64, sessionID string) {\\n\\t_, err := db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)\\n\\tif err != nil {\\n\\t\\tlog.Printf("DB Error updating session for user %d: %v", userID, err)\\n\\t}\\n}'
)
text = text.replace(
    'func updateUserModel(db *sql.DB, userID int64, model string) {\\n\\tdb.Exec("UPDATE users SET model = ? WHERE user_id = ?", model, userID)\\n}',
    'func updateUserModel(db *sql.DB, userID int64, model string) error {\\n\\t_, err := db.Exec("UPDATE users SET model = ? WHERE user_id = ?", model, userID)\\n\\tif err != nil {\\n\\t\\tlog.Printf("DB Error updating model for user %d: %v", userID, err)\\n\\t}\\n\\treturn err\\n}'
)
text = text.replace(
    'updateUserModel(db, userID, newModel)',
    'if err := updateUserModel(db, userID, newModel); err != nil {\\n\\t\\t\\t\\tbot.Send(tgbotapi.NewMessage(chatID, "❌ DB Error: "+err.Error()))\\n\\t\\t\\t\\treturn\\n\\t\\t\\t}'
)

# Fix 18: sessionKey instead of botName in globalSessions
text = re.sub(
    r'(func getSession\(botName string, user User\) \*AgySession \{)',
    r'\\1\\n\\tsessionKey := fmt.Sprintf("%s:%d", botName, user.ID)',
    text
)
text = re.sub(
    r'(botName := bot\.Self\.UserName\s*\\n\s*user := getUser\(db, userID, botName\))',
    r'\\1\\n\\tsessionKey := fmt.Sprintf("%s:%d", botName, userID)',
    text
)
text = text.replace('globalSessions[botName]', 'globalSessions[sessionKey]')
text = text.replace('delete(globalSessions, botName)', 'delete(globalSessions, sessionKey)')

# Fix 19: getAgentsDir
helper = """
func getAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/root/.agents"
	}
	return filepath.Join(home, ".agents")
}

func getSession"""
text = text.replace("func getSession", helper)
text = text.replace('Workspace:    fmt.Sprintf("/root/.agents/%s", botName),', 'Workspace:    filepath.Join(getAgentsDir(), botName),')
text = text.replace('"--add-dir", "/root/.agents/common",', '"--add-dir", filepath.Join(getAgentsDir(), "common"),')
text = text.replace('"--add-dir", "/root/.agents/" + s.BotName,', '"--add-dir", filepath.Join(getAgentsDir(), s.BotName),')
text = text.replace('agentDir := fmt.Sprintf("/root/.agents/%s", s.BotName)', 'agentDir := filepath.Join(getAgentsDir(), s.BotName)')
text = text.replace('downloadDir := fmt.Sprintf("/root/.agents/%s/scratch/downloads", botName)', 'downloadDir := filepath.Join(getAgentsDir(), botName, "scratch", "downloads")')
text = text.replace("workspace TEXT DEFAULT '/root/.agents',", "workspace TEXT DEFAULT '',")

# Fix 14: UpdateChan
text = re.sub(
    r'(LastEdit\s+time\.Time\\n)\}',
    r'\\1\\tUpdateChan      chan struct{}\\n}',
    text
)
text = re.sub(
    r'(Conversation:\s*user\.SessionID,)\\n\s*\}',
    r'\\1\\n\\t\\tUpdateChan:   make(chan struct{}, 1),\\n\\t}',
    text
)
text = re.sub(
    r'(Conversation:\s*convID,)\\n\s*\}',
    r'\\1\\n\\t\\t\\t\\tUpdateChan:   make(chan struct{}, 1),\\n\\t\\t\\t}',
    text
)
delta_handling = """				if delta, ok := su["text_delta"].(string); ok && delta != "" {
					s.mu.Lock()
					s.TextBuffer += delta
					s.mu.Unlock()
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}
				}"""
text = re.sub(
    r'if delta, ok := su\["text_delta"\].\(string\); ok && delta != "" \{\s*s\.TextBuffer \+= delta\s*\}',
    delta_handling,
    text
)
error_handling = """				if status, _ := res["status"].(string); status == "ERROR" {
					errMsg, _ := res["error"].(string)
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}"""
text = re.sub(
    r'if status, _ := res\["status"\].\(string\); status == "ERROR" \{\s*errMsg, _ := res\["error"\].\(string\)',
    error_handling,
    text
)
throttler = """		var lastSent string
		var lastSentTime time.Time
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.UpdateChan:
			case <-ticker.C:
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
					sendChunk(botAPI, chatID, activeMsgID, currentText+"\\\\n\\\\n*⏳ Typing...*")
					lastSent = currentText
					lastSentTime = time.Now()
				}
			}
		}"""
text = re.sub(
    r'var lastSent string\s*var lastSentTime time\.Time\s*for \{\s*s\.mu\.Lock\(\)[\s\S]*?time\.Sleep\(100 \* time\.Millisecond\).*?\\n\t\t\}',
    throttler,
    text
)

with open("agysessionsstarter.go", "w") as f:
    f.write(text)
