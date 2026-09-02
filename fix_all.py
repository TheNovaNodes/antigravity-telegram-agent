import re

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# 1. Add context
text = text.replace('"bytes"\n\t"database/sql"', '"bytes"\n\t"context"\n\t"database/sql"')
text = text.replace("UpdateChan      chan struct{}\n}", "UpdateChan      chan struct{}\n\tctx             context.Context\n\tcancel          context.CancelFunc\n\tInitChan        chan string\n}")

# 2. Add replaceSession helper
helper = """func replaceSession(botName string, user User, convID string, newModel string, newWorkspace string) *AgySession {
	sessionKey := fmt.Sprintf("%s:%d", botName, user.ID)
	sessionMu.Lock()
	defer sessionMu.Unlock()

	if old, ok := globalSessions[sessionKey]; ok {
		if old.cancel != nil {
			old.cancel()
		}
		if old.Cmd != nil && old.Cmd.Process != nil {
			old.Cmd.Process.Kill()
		}
		delete(globalSessions, sessionKey)
	}

	session := &AgySession{
		BotName:      botName,
		Model:        newModel,
		Workspace:    newWorkspace,
		Conversation: convID,
		UpdateChan:   make(chan struct{}, 1),
		InitChan:     make(chan string, 1),
	}

	session.start()
	globalSessions[sessionKey] = session
	return session
}

// getSession retrieves an active session for the user or creates a new isolated agent process.
"""
text = text.replace("// getSession retrieves an active session for the user or creates a new isolated agent process.\n", helper)

text = text.replace("""	session = &AgySession{
		BotName:      botName,
		Model:        user.Model,
		Workspace:    user.Workspace,
		Conversation: user.SessionID,
		UpdateChan:   make(chan struct{}, 1),
	}""", """	session = &AgySession{
		BotName:      botName,
		Model:        user.Model,
		Workspace:    user.Workspace,
		Conversation: user.SessionID,
		UpdateChan:   make(chan struct{}, 1),
		InitChan:     make(chan string, 1),
	}""")

# 3. start() context leak and Wait fix
text = re.sub(
    r"func \(s \*AgySession\) start\(\) \{",
    r"func (s *AgySession) start() {\n\tif s.cancel != nil {\n\t\ts.cancel()\n\t}\n\ts.ctx, s.cancel = context.WithCancel(context.Background())\n",
    text
)
text = re.sub(
    r"case <-ticker\.C:\n\t\t\t\}",
    r"case <-ticker.C:\n\t\t\tcase <-s.ctx.Done():\n\t\t\t\treturn\n\t\t\t}",
    text
)
old_wait = """	go func() {
		s.Cmd.Wait()
		s.mu.Lock()
		s.Cmd = nil
		s.mu.Unlock()
	}()"""
new_wait = """	go func(cmd *exec.Cmd) {
		cmd.Wait()
		s.mu.Lock()
		if s.Cmd == cmd {
			s.Cmd = nil
		}
		s.mu.Unlock()
	}(s.Cmd)"""
text = text.replace(old_wait, new_wait)
text = text.replace("""func (s *AgySession) Restart() {
	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
	}
	s.start()
}""", """func (s *AgySession) Restart() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.Cmd != nil && s.Cmd.Process != nil {
		s.Cmd.Process.Kill()
	}
	s.start()
}""")

# 4. readStdoutLoop races
err_old = """					if s.ActiveMessageID == 0 {
						msg := tgbotapi.NewMessage(s.ChatID, "❌ Error from agent: "+errMsg)
						s.BotAPI.Send(msg)
					} else {
						sendChunk(s.BotAPI, s.ChatID, s.ActiveMessageID, "❌ Error from agent: "+errMsg)
					}

					// Auto-heal: kill the broken process so it restarts on the next message
					s.mu.Lock()
					if s.Cmd != nil && s.Cmd.Process != nil {
						s.Cmd.Process.Kill()
					}
					s.mu.Unlock()

					s.ActiveMessageID = 0
					s.TextBuffer = ""
					continue"""
err_new = """					s.mu.Lock()
					activeMsgID := s.ActiveMessageID
					s.mu.Unlock()
					if activeMsgID == 0 {
						msg := tgbotapi.NewMessage(s.ChatID, "❌ Error from agent: "+errMsg)
						s.BotAPI.Send(msg)
					} else {
						sendChunk(s.BotAPI, s.ChatID, activeMsgID, "❌ Error from agent: "+errMsg)
					}

					s.mu.Lock()
					if s.Cmd != nil && s.Cmd.Process != nil {
						s.Cmd.Process.Kill()
					}
					s.ActiveMessageID = 0
					s.TextBuffer = ""
					s.mu.Unlock()
					continue"""
text = text.replace(err_old, err_new)

resp_old = """				response := s.TextBuffer
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
"""
resp_new = """				s.mu.Lock()
				response := s.TextBuffer
				activeMsgID := s.ActiveMessageID
				s.mu.Unlock()
				
				if response == "" {
					response = "No response from agent."
				}
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
				
				s.mu.Lock()
				s.ActiveMessageID = 0
				s.TextBuffer = ""
				s.mu.Unlock()
"""
text = text.replace(resp_old, resp_new)

update_old = """		if session.ActiveMessageID == 0 {
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
		}"""
update_new = """		session.mu.Lock()
		activeMsgID := session.ActiveMessageID
		session.mu.Unlock()
		if activeMsgID == 0 {
			msg := tgbotapi.NewMessage(chatID, "*⏳ Thinking...*")
			msg.ParseMode = "Markdown"
			sentMsg, err := bot.Send(msg)
			if err == nil {
				session.mu.Lock()
				session.ActiveMessageID = sentMsg.MessageID
				session.mu.Unlock()
			}
		} else {
			msg := tgbotapi.NewMessage(chatID, "⏳ _Message queued..._")
			msg.ParseMode = "Markdown"
			bot.Send(msg)
		}"""
text = text.replace(update_old, update_new)

# 5. Handlers TOCTOU and init race
resume_old = """			sessionMu.Lock()
			if old, ok := globalSessions[sessionKey]; ok {
				if old.Cmd != nil && old.Cmd.Process != nil {
					old.Cmd.Process.Kill()
				}
				delete(globalSessions, sessionKey)
			}
			sessionMu.Unlock()

			updateUserSession(db, userID, convID)

			session := &AgySession{
				BotName:      botName,
				Model:        user.Model,
				Workspace:    user.Workspace,
				Conversation: convID,
				UpdateChan:   make(chan struct{}, 1),
			}
			session.start()
			sessionMu.Lock()
			globalSessions[sessionKey] = session
			sessionMu.Unlock()

			// Read the init event to confirm
			if session.StdoutScanner.Scan() {
				var initData map[string]interface{}
				if json.Unmarshal([]byte(session.StdoutScanner.Text()), &initData) == nil {
					session.Conversation, _ = initData["conversation_id"].(string)
				}
			}"""
resume_new = """			updateUserSession(db, userID, convID)
			session := replaceSession(botName, user, convID, user.Model, user.Workspace)

			select {
			case newID := <-session.InitChan:
				session.Conversation = newID
				convID = newID
			case <-time.After(3 * time.Second):
			}"""
text = text.replace(resume_old, resume_new)

init_old = """		event, _ := data["event"].(string)
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
		}"""
init_new = """		event, _ := data["event"].(string)
		if event == "init" {
			newID, _ := data["conversation_id"].(string)
			if newID != "" {
				select {
				case s.InitChan <- newID:
				default:
				}
				if newID != s.Conversation {
					s.Conversation = newID
					// Update DB
					dbPath := fmt.Sprintf("sessions_%s.db", s.BotName)
					if localDB, err := sql.Open("sqlite3", dbPath); err == nil {
						localDB.Exec("UPDATE users SET session_id = ? WHERE chat_id = ?", newID, s.ChatID)
						localDB.Close()
					}
				}
			}
		}"""
text = text.replace(init_old, init_new)

model_old = """			sessionMu.Lock()
			if old, ok := globalSessions[sessionKey]; ok {
				if old.Cmd != nil && old.Cmd.Process != nil {
					old.Cmd.Process.Kill()
				}
				delete(globalSessions, sessionKey)
			}
			sessionMu.Unlock()"""
model_new = """			replaceSession(botName, user, user.SessionID, newModel, user.Workspace)"""
text = text.replace(model_old, model_new)

clear_old = """		sessionMu.Lock()
		if old, ok := globalSessions[sessionKey]; ok {
			if old.Cmd != nil && old.Cmd.Process != nil {
				old.Cmd.Process.Kill()
			}
			delete(globalSessions, sessionKey)
		}
		sessionMu.Unlock()

		newUUID := uuid.New().String()"""
clear_new = """		newUUID := uuid.New().String()
		replaceSession(botName, user, newUUID, user.Model, user.Workspace)"""
text = text.replace(clear_old, clear_new)

# Workspace
workspace_old = """		sessionMu.Lock()
		if old, ok := globalSessions[sessionKey]; ok {
			if old.Cmd != nil && old.Cmd.Process != nil {
				old.Cmd.Process.Kill()
			}
			delete(globalSessions, sessionKey)
		}
		sessionMu.Unlock()"""
workspace_new = """		replaceSession(botName, user, user.SessionID, user.Model, newWS)"""
text = text.replace(workspace_old, workspace_new)

with open("agysessionsstarter.go", "w") as f:
    f.write(text)
