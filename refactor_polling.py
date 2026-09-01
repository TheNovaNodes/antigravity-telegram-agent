import re

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# 1. Add UpdateChan to AgySession
text = re.sub(
    r'(LastEdit\s+time\.Time\n)\}',
    r'\1\tUpdateChan      chan struct{}\n}',
    text
)

# 2. Initialize UpdateChan in getSession
text = re.sub(
    r'(Conversation:\s*user\.SessionID,)\n\s*\}',
    r'\1\n\t\tUpdateChan:   make(chan struct{}, 1),\n\t}',
    text
)

# 3. Initialize UpdateChan in handleCallbackQuery (resume)
text = re.sub(
    r'(Conversation:\s*convID,)\n\s*\}',
    r'\1\n\t\t\t\tUpdateChan:   make(chan struct{}, 1),\n\t\t\t}',
    text
)

# 4. Fix readStdoutLoop text_delta handling and send to channel
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

# 5. Also wake up on result ERROR
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

# 6. Refactor the throttler loop in getSession (it's inside start())
# Let's find the loop. Wait, the loop is in func (s *AgySession) start()
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
					sendChunk(botAPI, chatID, activeMsgID, currentText+"\\n\\n*⏳ Typing...*")
					lastSent = currentText
					lastSentTime = time.Now()
				}
			}
		}"""
text = re.sub(
    r'var lastSent string\s*var lastSentTime time\.Time\s*for \{\s*s\.mu\.Lock\(\)[\s\S]*?time\.Sleep\(100 \* time\.Millisecond\).*?\n\t\t\}',
    throttler,
    text
)


with open("agysessionsstarter.go", "w") as f:
    f.write(text)
