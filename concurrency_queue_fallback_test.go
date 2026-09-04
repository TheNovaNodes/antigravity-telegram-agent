package main

import (
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestFallbackModel_TerminalExhaustion(t *testing.T) {
	// Verify non-cyclic terminal fallback chain
	m1 := getFallbackModel("gemini-3.8-flash-high")
	if m1 != "gemini-3.7-flash-high" {
		t.Errorf("Expected gemini-3.7-flash-high, got %s", m1)
	}

	m2 := getFallbackModel(m1)
	if m2 != "gemini-3.1-pro-high" {
		t.Errorf("Expected gemini-3.1-pro-high, got %s", m2)
	}

	m3 := getFallbackModel(m2)
	if m3 != "gemini-3.6-flash-low" {
		t.Errorf("Expected gemini-3.6-flash-low, got %s", m3)
	}

	m4 := getFallbackModel(m3)
	if m4 != "" {
		t.Errorf("Expected terminal empty fallback (exhaustion), got %s (infinite loop detected!)", m4)
	}
}

func TestDispatchUpdate_SequentialPerChat(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(8888)
	userID := int64(7777)
	const messageCount = 15

	for i := 0; i < messageCount; i++ {
		update := tgbotapi.Update{
			UpdateID: 5000 + i,
			Message: &tgbotapi.Message{
				MessageID: 500 + i,
				Chat:      &tgbotapi.Chat{ID: chatID},
				From:      &tgbotapi.User{ID: userID},
				Text:      "/help",
			},
		}
		dispatchUpdate(bot, update, db)
	}

	// Allow worker queue to drain
	time.Sleep(300 * time.Millisecond)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		session.Kill()
	}
}

func TestConcurrentChatQueues_MultiChatThroughput(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	var wg sync.WaitGroup
	const chatCount = 10
	const msgsPerChat = 5

	for c := 0; c < chatCount; c++ {
		wg.Add(1)
		go func(chatIdx int) {
			defer wg.Done()
			cID := int64(10000 + chatIdx)
			uID := int64(20000 + chatIdx)
			for m := 0; m < msgsPerChat; m++ {
				update := tgbotapi.Update{
					UpdateID: 6000 + chatIdx*100 + m,
					Message: &tgbotapi.Message{
						MessageID: 600 + chatIdx*100 + m,
						Chat:      &tgbotapi.Chat{ID: cID},
						From:      &tgbotapi.User{ID: uID},
						Text:      "/help",
					},
				}
				dispatchUpdate(bot, update, db)
			}
		}(c)
	}

	wg.Wait()
	time.Sleep(400 * time.Millisecond)
}
