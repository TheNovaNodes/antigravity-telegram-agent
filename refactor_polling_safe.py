import sys

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# 1. Add UpdateChan
text = text.replace(
    'LastEdit        time.Time\n}',
    'LastEdit        time.Time\n\tUpdateChan      chan struct{}\n}'
)

# 2. Init UpdateChan in getSession
text = text.replace(
    'Conversation: user.SessionID,\n\t}',
    'Conversation: user.SessionID,\n\t\tUpdateChan:   make(chan struct{}, 1),\n\t}'
)

# 3. Init UpdateChan in handleCallbackQuery
text = text.replace(
    'Conversation: convID,\n\t\t\t}',
    'Conversation: convID,\n\t\t\t\tUpdateChan:   make(chan struct{}, 1),\n\t\t\t}'
)

# 4. readStdoutLoop text_delta
old_delta = """				if delta, ok := su["text_delta"].(string); ok && delta != "" {
					s.TextBuffer += delta
				}"""
new_delta = """				if delta, ok := su["text_delta"].(string); ok && delta != "" {
					s.mu.Lock()
					s.TextBuffer += delta
					s.mu.Unlock()
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}
				}"""
text = text.replace(old_delta, new_delta)

# 5. readStdoutLoop result error
old_error = """				if status, _ := res["status"].(string); status == "ERROR" {
					errMsg, _ := res["error"].(string)"""
new_error = """				if status, _ := res["status"].(string); status == "ERROR" {
					errMsg, _ := res["error"].(string)
					select {
					case s.UpdateChan <- struct{}{}:
					default:
					}"""
text = text.replace(old_error, new_error)

# 6. Throttler loop
old_throttler = """		var lastSent string
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
					sendChunk(botAPI, chatID, activeMsgID, currentText+"\\n\\n*⏳ Typing...*")
					lastSent = currentText
					lastSentTime = time.Now()
				}
			}
			time.Sleep(100 * time.Millisecond) // Fast polling, rate limited sending
		}"""
new_throttler = """		var lastSent string
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
					sendChunk(botAPI, chatID, activeMsgID, currentText+"\\n\\n*⏳ Typing...*")
					lastSent = currentText
					lastSentTime = time.Now()
				}
			}
		}"""
text = text.replace(old_throttler, new_throttler)

with open("agysessionsstarter.go", "w") as f:
    f.write(text)
