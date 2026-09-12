package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestDownloadTelegramMedia_SuccessWithPlaceholderID(t *testing.T) {
	fileContent := "hello world media content"
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fileContent))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "getFile") {
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"f123","file_path":"%s"}}`, fileServer.URL+"/test.txt")))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			w.Write([]byte(`{"ok":true,"result":{"message_id":301,"chat":{"id":12345}}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":301}}`))
	})

	bot := createMockBot(ms)
	tempAgents := t.TempDir()
	t.Setenv("AGENTS_DIR", tempAgents)

	formatted, isFile, placeholderID, err := downloadTelegramMedia(bot, 12345, "f123", ".txt", "user prompt", "user caption", "TestBot")
	if err != nil {
		t.Fatalf("downloadTelegramMedia failed unexpectedly: %v", err)
	}
	if !isFile {
		t.Fatalf("expected isFile to be true")
	}
	if placeholderID != 301 {
		t.Fatalf("expected placeholderID 301, got %d", placeholderID)
	}
	if !strings.Contains(formatted, "[Attached File: file://") {
		t.Errorf("expected attached file prefix, got: %s", formatted)
	}
	if !strings.Contains(formatted, "user prompt") {
		t.Errorf("expected user prompt preserved, got: %s", formatted)
	}
}

func TestDownloadTelegramMedia_ErrorEditsPlaceholder(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var editCount int32
	var lastEditText string
	var editMu sync.Mutex

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "getFile") {
			// Point to an invalid unreachable port to trigger download failure
			w.Write([]byte(`{"ok":true,"result":{"file_id":"bad_file","file_path":"http://127.0.0.1:59999/does_not_exist"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			w.Write([]byte(`{"ok":true,"result":{"message_id":402,"chat":{"id":12345}}}`))
			return
		}
		if strings.Contains(r.URL.Path, "editMessageText") {
			atomic.AddInt32(&editCount, 1)
			editMu.Lock()
			if len(ms.sentBodies) > 0 {
				lastEditText = ms.sentBodies[len(ms.sentBodies)-1]
			}
			editMu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":402}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":402}}`))
	})

	bot := createMockBot(ms)
	tempAgents := t.TempDir()
	t.Setenv("AGENTS_DIR", tempAgents)

	_, _, placeholderID, err := downloadTelegramMedia(bot, 12345, "bad_file", ".txt", "", "", "TestBot")
	if err == nil {
		t.Fatalf("expected download error, got nil")
	}
	if placeholderID != 402 {
		t.Errorf("expected placeholderID 402, got %d", placeholderID)
	}
	if atomic.LoadInt32(&editCount) == 0 {
		t.Errorf("expected placeholder message to be edited with error, but editMessageText was not called")
	}
	editMu.Lock()
	savedText := lastEditText
	editMu.Unlock()
	decodedText, _ := url.QueryUnescape(savedText)
	if !strings.Contains(decodedText, "Failed to download") {
		t.Errorf("expected edit message to contain failure text, got: %s (raw: %s)", decodedText, savedText)
	}
}

func TestSendOrEditError(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	var calledEdit, calledSend bool
	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "editMessageText") {
			calledEdit = true
			w.Write([]byte(`{"ok":true,"result":{"message_id":55}}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			calledSend = true
			w.Write([]byte(`{"ok":true,"result":{"message_id":56}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":55}}`))
	})

	bot := createMockBot(ms)

	// Case 1: placeholderMsgID == 0 -> should call sendMessage
	calledEdit = false
	calledSend = false
	sendOrEditError(bot, 12345, 0, "test error 1")
	if !calledSend || calledEdit {
		t.Errorf("expected sendMessage call when placeholderMsgID=0, got send=%v, edit=%v", calledSend, calledEdit)
	}

	// Case 2: placeholderMsgID > 0 -> should call editMessageText
	calledEdit = false
	calledSend = false
	sendOrEditError(bot, 12345, 55, "test error 2")
	if !calledEdit || calledSend {
		t.Errorf("expected editMessageText call when placeholderMsgID=55, got send=%v, edit=%v", calledSend, calledEdit)
	}
}

func TestHandleMessagePayload_AdoptsPlaceholderID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	var editedText string
	var mu sync.Mutex

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "editMessageText") {
			mu.Lock()
			if len(ms.sentBodies) > 0 {
				body := ms.sentBodies[len(ms.sentBodies)-1]
				editedText = body
			}
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":888}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":888}}`))
	})

	bot := createMockBot(ms)
	chatID := int64(88888)
	user := User{ID: 999}

	session := getSession("TestBot", user, chatID, db)
	defer func() {
		session.Kill()
		sessionMu.Lock()
		delete(globalSessions, fmt.Sprintf("TestBot:%d:%d", chatID, user.ID))
		sessionMu.Unlock()
	}()
	session.mu.Lock()
	session.isAlive = true // prevent actual agy spawn
	rPipe, wPipe, _ := os.Pipe()
	session.Stdin = wPipe
	session.ActiveMessageID = 0
	session.mu.Unlock()
	defer rPipe.Close()
	defer wPipe.Close()

	// Call handleMessagePayload with placeholderID = 888
	handleMessagePayload(bot, chatID, user.ID, "Prompt from media attachment", "TestBot", user, false, true, db, 888)

	session.mu.Lock()
	activeID := session.ActiveMessageID
	hasStart := !session.ActiveTurnStart.IsZero()
	session.mu.Unlock()

	if activeID != 888 {
		t.Fatalf("expected session.ActiveMessageID to be adopted as 888, got %d", activeID)
	}
	if !hasStart {
		t.Errorf("expected ActiveTurnStart to be set")
	}

	mu.Lock()
	gotEditedText := editedText
	mu.Unlock()

	if !strings.Contains(gotEditedText, "Thinking") {
		t.Errorf("expected editMessageText to morph placeholder into Thinking..., got body: %s", gotEditedText)
	}
}

func TestHandleMessagePayload_VoiceReplyChatAction(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	var chatActionSent string
	var mu sync.Mutex

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "sendChatAction") {
			mu.Lock()
			if len(ms.sentBodies) > 0 {
				chatActionSent = ms.sentBodies[len(ms.sentBodies)-1]
			}
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":123}}`))
	})

	bot := createMockBot(ms)
	chatID := int64(99999)
	user := User{ID: 888}

	session := getSession("TestVoiceBot", user, chatID, db)
	defer func() {
		session.Kill()
		sessionMu.Lock()
		delete(globalSessions, fmt.Sprintf("TestVoiceBot:%d:%d", chatID, user.ID))
		sessionMu.Unlock()
	}()
	session.mu.Lock()
	session.isAlive = true
	rPipe, wPipe, _ := os.Pipe()
	session.Stdin = wPipe
	session.ActiveMessageID = 0
	session.mu.Unlock()
	defer rPipe.Close()
	defer wPipe.Close()

	// Call with isVoice = true
	handleMessagePayload(bot, chatID, user.ID, "Transcribed voice note", "TestVoiceBot", user, true, true, db, 123)

	session.mu.Lock()
	isVoiceReply := session.VoiceReply
	session.mu.Unlock()

	if !isVoiceReply {
		t.Errorf("expected session.VoiceReply to be true")
	}

	mu.Lock()
	actionBody := chatActionSent
	mu.Unlock()

	if !strings.Contains(actionBody, "record_voice") {
		t.Errorf("expected sendChatAction with record_voice, got: %s", actionBody)
	}
}

func TestDispatchUpdate_ConcurrentSafety(t *testing.T) {
	t.Setenv("AGY_BINARY", "cat")
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(777111)
	defer func() {
		chatQueuesMu.Lock()
		delete(chatQueues, chatID)
		chatQueuesMu.Unlock()
	}()
	const numTasks = 50

	var wg sync.WaitGroup
	wg.Add(numTasks)

	for i := 0; i < numTasks; i++ {
		go func(idx int) {
			defer wg.Done()
			update := tgbotapi.Update{
				UpdateID: idx,
				Message: &tgbotapi.Message{
					MessageID: idx + 100,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: 12345},
					Text:      "/help",
				},
			}
			dispatchUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	chatQueuesMu.Lock()
	_, exists := chatQueues[chatID]
	chatQueuesMu.Unlock()

	if !exists {
		t.Errorf("expected active chat queue to exist for chatID %d", chatID)
	}
}
