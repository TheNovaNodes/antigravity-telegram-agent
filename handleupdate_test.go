package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type mockServer struct {
	server        *httptest.Server
	mu            sync.Mutex
	sentRequests  []*http.Request
	sentBodies    []string
	customHandler http.HandlerFunc
}

func newMockServer() *mockServer {
	ms := &mockServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		customH := ms.customHandler
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		ms.sentRequests = append(ms.sentRequests, r)
		ms.sentBodies = append(ms.sentBodies, string(bodyBytes))
		ms.mu.Unlock()

		if customH != nil {
			customH(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/bot123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11/getMe" {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"TestMockBot","username":"TestMockBot"}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"mocked"}}`))
	})
	ms.server = httptest.NewServer(handler)
	return ms
}

func (ms *mockServer) Close() {
	ms.server.Close()
}

func createMockBot(ms *mockServer) *tgbotapi.BotAPI {
	endpoint := ms.server.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ms.server.Client())
	if err != nil {
		panic(err)
	}
	return bot
}

func TestHandleUpdate_Commands(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	commands := []string{
		"/start",
		"/help",
		"/model",
		"/usage",
		"/clear",
		"/refresh_models",
		"/rename TestSessionTitle",
	}

	for _, cmd := range commands {
		t.Run("Command "+cmd, func(t *testing.T) {
			ms.mu.Lock()
			startCount := len(ms.sentRequests)
			ms.mu.Unlock()

			update := tgbotapi.Update{
				UpdateID: 1,
				Message: &tgbotapi.Message{
					MessageID: 10,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
					Text:      cmd,
				},
			}

			handleUpdate(bot, update, db)

			ms.mu.Lock()
			newCount := len(ms.sentRequests) - startCount
			ms.mu.Unlock()

			if newCount == 0 {
				t.Errorf("Expected Telegram request to be sent for command %s", cmd)
			}
		})
	}
}

func TestHandleUpdate_Workspace(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	// Test 1: Invalid workspace (not absolute)
	updateInvalid := tgbotapi.Update{
		UpdateID: 2,
		Message: &tgbotapi.Message{
			MessageID: 11,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/workspace relative/path",
		},
	}
	handleUpdate(bot, updateInvalid, db)

	// Test 2: Valid workspace under allowed root
	validWS := filepath.Join(getAgentsDir(), "TestMockBot", "lab")
	os.MkdirAll(validWS, 0755)
	updateValid := tgbotapi.Update{
		UpdateID: 3,
		Message: &tgbotapi.Message{
			MessageID: 12,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/workspace " + validWS,
		},
	}
	handleUpdate(bot, updateValid, db)
}

func TestHandleUpdate_Callbacks(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	callbacks := []string{
		"cmd:status",
		"cmd:clear",
		"cmd:help",
		"cmd:usage",
		"cmd:model",
		"model:gemini-3.8-flash-high",
		"ans:SelectedOptionA",
	}

	for _, cb := range callbacks {
		t.Run("Callback "+cb, func(t *testing.T) {
			ms.mu.Lock()
			startCount := len(ms.sentRequests)
			ms.mu.Unlock()

			update := tgbotapi.Update{
				UpdateID: 10,
				CallbackQuery: &tgbotapi.CallbackQuery{
					ID:   "cb123",
					From: &tgbotapi.User{ID: userID},
					Message: &tgbotapi.Message{
						MessageID: 20,
						Chat:      &tgbotapi.Chat{ID: chatID},
					},
					Data: cb,
				},
			}

			handleUpdate(bot, update, db)

			ms.mu.Lock()
			newCount := len(ms.sentRequests) - startCount
			ms.mu.Unlock()

			if newCount == 0 {
				t.Errorf("Expected request sent for callback %s", cb)
			}
		})
	}
}

func TestHandleUpdate_UnsupportedMedia(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	// Message with no text, caption, or supported media
	update := tgbotapi.Update{
		UpdateID: 100,
		Message: &tgbotapi.Message{
			MessageID: 30,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
		},
	}

	handleUpdate(bot, update, db)
}

func TestHandleUpdate_TextMessage_Stream(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	update := tgbotapi.Update{
		UpdateID: 200,
		Message: &tgbotapi.Message{
			MessageID: 40,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "Please write a hello world program in Go.",
		},
	}

	handleUpdate(bot, update, db)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestHandleUpdate_AfterRateLimit_CleanResume(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(888)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	session.Kill()

	// Simulate rate limit 429 arriving on stdout stream for this session
	rateLimitJSONL := `{"event":"result","result":{"status":"ERROR","error":"429 Resource has been exhausted (e.g. check quota)."}}` + "\n"
	rateLimitSession := &AgySession{
		BotName:       "TestMockBot",
		Model:         defaultModel,
		Workspace:     user.Workspace,
		Conversation:  user.SessionID,
		UserID:        userID,
		ChatID:        chatID,
		BotAPI:        bot,
		UpdateChan:    make(chan struct{}, 10),
		InitChan:      make(chan string, 10),
		StdoutScanner: bufio.NewScanner(strings.NewReader(rateLimitJSONL)),
	}
	rateLimitSession.ctx, rateLimitSession.cancel = context.WithCancel(context.Background())
	rateLimitSession.readStdoutLoop()

	// Verify ActiveMessageID is reset to 0
	rateLimitSession.mu.Lock()
	activeID := rateLimitSession.ActiveMessageID
	rateLimitSession.mu.Unlock()
	if activeID != 0 {
		t.Errorf("Expected ActiveMessageID to be 0 after rate limit, got %d", activeID)
	}

	// Now simulate user sending a new message after rate limit window passed
	ms.mu.Lock()
	startReqCount := len(ms.sentRequests)
	ms.mu.Unlock()

	update := tgbotapi.Update{
		UpdateID: 300,
		Message: &tgbotapi.Message{
			MessageID: 50,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "Are you available now?",
		},
	}

	handleUpdate(bot, update, db)

	ms.mu.Lock()
	newReqCount := len(ms.sentRequests) - startReqCount
	ms.mu.Unlock()

	if newReqCount == 0 {
		t.Error("Expected Telegram messages to be sent for new prompt after rate limit")
	}

	resumedSession := getSession("TestMockBot", user, chatID)
	if resumedSession != nil {
		resumedSession.Kill()
	}
}

func TestHandleCommand_VoiceToggle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(888)

	// Toggle ON
	updateOn := tgbotapi.Update{
		UpdateID: 401,
		Message: &tgbotapi.Message{
			MessageID: 51,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/voice on",
		},
	}
	handleUpdate(bot, updateOn, db)

	u := getUser(db, userID, "TestMockBot")
	if !u.VoiceReply {
		t.Errorf("Expected user VoiceReply to be true after /voice on")
	}

	// Toggle OFF
	updateOff := tgbotapi.Update{
		UpdateID: 402,
		Message: &tgbotapi.Message{
			MessageID: 52,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/voice off",
		},
	}
	handleUpdate(bot, updateOff, db)

	u = getUser(db, userID, "TestMockBot")
	if u.VoiceReply {
		t.Errorf("Expected user VoiceReply to be false after /voice off")
	}
}

func TestHandleCommand_TTS_NoKey(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(888)

	os.Unsetenv("ELEVENLABS_API_KEY")

	updateTTS := tgbotapi.Update{
		UpdateID: 403,
		Message: &tgbotapi.Message{
			MessageID: 53,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/tts Hello world",
		},
	}
	handleUpdate(bot, updateTTS, db)

	// Empty TTS
	updateEmpty := tgbotapi.Update{
		UpdateID: 404,
		Message: &tgbotapi.Message{
			MessageID: 54,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/tts",
		},
	}
	handleUpdate(bot, updateEmpty, db)
}

func TestHandleCommand_Resume_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(888)

	update := tgbotapi.Update{
		UpdateID: 405,
		Message: &tgbotapi.Message{
			MessageID: 55,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "/resume",
		},
	}
	handleUpdate(bot, update, db)
}

func TestHandleCallbackQuery_HotModelSwap_PreservesContext(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(999)

	// 1. Initial user setup
	initialUser := getUser(db, userID, "TestMockBot")
	initialSessionID := initialUser.SessionID

	// 2. Switch model via callback
	cbUpdate := tgbotapi.Update{
		UpdateID: 501,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID:   "cb_model_swap",
			From: &tgbotapi.User{ID: userID},
			Message: &tgbotapi.Message{
				MessageID: 25,
				Chat:      &tgbotapi.Chat{ID: chatID},
			},
			Data: "model:gemini-3.1-pro-high",
		},
	}
	handleUpdate(bot, cbUpdate, db)

	// 3. Verify user in DB still has the exact same SessionID (context preserved)
	updatedUser := getUser(db, userID, "TestMockBot")
	if updatedUser.SessionID != initialSessionID {
		t.Errorf("Expected SessionID to be preserved %s, but got %s", initialSessionID, updatedUser.SessionID)
	}
	if updatedUser.Model != "gemini-3.1-pro-high" {
		t.Errorf("Expected model to be gemini-3.1-pro-high, got %s", updatedUser.Model)
	}

	// 4. Verify session instance in globalSessions retains the conversation ID
	session := getSession("TestMockBot", updatedUser, chatID)
	if session.GetConversation() != initialSessionID {
		t.Errorf("Expected session conversation to be %s, got %s", initialSessionID, session.GetConversation())
	}
	if session.Model != "gemini-3.1-pro-high" {
		t.Errorf("Expected session model to be gemini-3.1-pro-high, got %s", session.Model)
	}

	session.Kill()
}

func TestHotModelSwap_EndToEnd_MultiTurnPipeline(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(778899)
	userID := int64(778899)

	// Turn 1: User sends message on default model
	update1 := tgbotapi.Update{
		UpdateID: 601,
		Message: &tgbotapi.Message{
			MessageID: 101,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "Step 1: Compute matrix decomposition",
		},
	}
	handleUpdate(bot, update1, db)

	userTurn1 := getUser(db, userID, "TestMockBot")
	sessionTurn1 := getSession("TestMockBot", userTurn1, chatID)
	initialConvID := sessionTurn1.GetConversation()

	if initialConvID == "" {
		t.Fatal("Expected active conversation ID for Turn 1")
	}

	// Hot Model Swap: User selects Claude 3.7 Sonnet
	swapUpdate := tgbotapi.Update{
		UpdateID: 602,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID:   "cb_swap_sonnet",
			From: &tgbotapi.User{ID: userID},
			Message: &tgbotapi.Message{
				MessageID: 102,
				Chat:      &tgbotapi.Chat{ID: chatID},
			},
			Data: "model:claude-3-7-sonnet",
		},
	}
	handleUpdate(bot, swapUpdate, db)

	// Verify DB and session consistency
	userSwapped := getUser(db, userID, "TestMockBot")
	if userSwapped.Model != "claude-3-7-sonnet" {
		t.Errorf("Expected model to be claude-3-7-sonnet, got %s", userSwapped.Model)
	}
	if userSwapped.SessionID != initialConvID {
		t.Errorf("Expected SessionID to remain %s, got %s", initialConvID, userSwapped.SessionID)
	}

	sessionSwapped := getSession("TestMockBot", userSwapped, chatID)
	if sessionSwapped.Model != "claude-3-7-sonnet" {
		t.Errorf("Expected session model to be claude-3-7-sonnet, got %s", sessionSwapped.Model)
	}
	if sessionSwapped.GetConversation() != initialConvID {
		t.Errorf("Expected session conversation to remain %s, got %s", initialConvID, sessionSwapped.GetConversation())
	}

	// Turn 2: User continues conversation on new model
	update2 := tgbotapi.Update{
		UpdateID: 603,
		Message: &tgbotapi.Message{
			MessageID: 103,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID},
			Text:      "Step 2: Continue matrix decomposition with new model",
		},
	}
	handleUpdate(bot, update2, db)

	sessionSwapped.Kill()
}

func TestHotModelSwap_ConcurrentSwaps_ThreadSafety(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	const concurrency = 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			userID := int64(2000 + idx)
			chatID := int64(3000 + idx)
			newModel := fmt.Sprintf("model-tier-%d", idx%3)

			// Setup initial user
			u := getUser(db, userID, "TestMockBot")
			origSessionID := u.SessionID

			// Dispatch swap callback
			cbUpdate := tgbotapi.Update{
				UpdateID: idx,
				CallbackQuery: &tgbotapi.CallbackQuery{
					ID:   fmt.Sprintf("cb_%d", idx),
					From: &tgbotapi.User{ID: userID},
					Message: &tgbotapi.Message{
						MessageID: 50,
						Chat:      &tgbotapi.Chat{ID: chatID},
					},
					Data: "model:" + newModel,
				},
			}
			handleUpdate(bot, cbUpdate, db)

			// Assert preservation
			afterUser := getUser(db, userID, "TestMockBot")
			if afterUser.SessionID != origSessionID {
				t.Errorf("User %d: Expected SessionID %s, got %s", userID, origSessionID, afterUser.SessionID)
			}
			if afterUser.Model != newModel {
				t.Errorf("User %d: Expected model %s, got %s", userID, newModel, afterUser.Model)
			}

			done <- true
		}(i)
	}

	for i := 0; i < concurrency; i++ {
		<-done
	}
}

func TestHandleCallbackQuery_Model_DBError(t *testing.T) {
	db := setupTestDB(t)
	// Close DB immediately to induce failure
	db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	user := User{
		ID:        999,
		Workspace: "/root",
		Model:     "gemini-3.8-flash-high",
		SessionID: "uuid-123",
	}

	cb := &tgbotapi.CallbackQuery{
		ID:   "cb_fail",
		From: &tgbotapi.User{ID: user.ID},
		Message: &tgbotapi.Message{
			MessageID: 10,
			Chat:      &tgbotapi.Chat{ID: 12345},
		},
		Data: "model:gemini-3.1-pro-high",
	}

	// Should not panic, but gracefully return DB error to chat
	handleCallbackQuery(bot, cb, user, "TestMockBot", db)
}

func TestSendChunk_EmptyAndWhitespaceSafe(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	testCases := []struct {
		name string
		text string
	}{
		{"Empty string", ""},
		{"Whitespace only", "   \n\t  "},
		{"Lone code fence", "```\n```"},
		{"Empty think block", "<think></think>"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := sendChunk(bot, 12345, 10, tc.text)
			if len(chunks) == 0 {
				t.Fatalf("Expected at least 1 chunk, got 0")
			}
			for i, chunk := range chunks {
				if strings.TrimSpace(chunk) == "" {
					t.Errorf("Chunk %d is empty or whitespace for input %q", i, tc.text)
				}
			}
		})
	}
}
