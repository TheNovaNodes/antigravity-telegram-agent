package main

import (
	"os"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestConcurrentClearSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(999)

	var wg sync.WaitGroup
	const concurrency = 25

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			update := tgbotapi.Update{
				UpdateID: 1000 + idx,
				Message: &tgbotapi.Message{
					MessageID: 100 + idx,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: userID},
					Text:      "/clear",
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestConcurrentMessageSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(54321)
	userID := int64(1111)

	var wg sync.WaitGroup
	const concurrency = 25

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			update := tgbotapi.Update{
				UpdateID: 2000 + idx,
				Message: &tgbotapi.Message{
					MessageID: 200 + idx,
					Chat:      &tgbotapi.Chat{ID: chatID},
					From:      &tgbotapi.User{ID: userID},
					Text:      "Concurrent stream test message",
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestConcurrentModelSwitchSpam(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(67890)
	userID := int64(2222)

	var wg sync.WaitGroup
	const concurrency = 20

	models := []string{
		"model:gemini-3.7-flash-high",
		"model:gemini-3.1-pro-high",
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			selectedModel := models[idx%len(models)]
			update := tgbotapi.Update{
				UpdateID: 3000 + idx,
				CallbackQuery: &tgbotapi.CallbackQuery{
					ID: "cb_id",
					Message: &tgbotapi.Message{
						MessageID: 300 + idx,
						Chat:      &tgbotapi.Chat{ID: chatID},
					},
					From: &tgbotapi.User{ID: userID},
					Data: selectedModel,
				},
			}
			handleUpdate(bot, update, db)
		}(i)
	}

	wg.Wait()

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestHandleUpdate_PanicRecovery(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)

	update := tgbotapi.Update{
		UpdateID: 9999,
		Message: &tgbotapi.Message{
			MessageID: 999,
			Chat:      &tgbotapi.Chat{ID: 1},
			From:      &tgbotapi.User{ID: 1},
			Text:      "/start",
		},
	}

	// Should recover gracefully from nil db and not panic the test
	handleUpdate(bot, update, nil)
}
