package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestWebhookGuard_DefaultAllowedUpdates(t *testing.T) {
	requiredTypes := []string{"message", "edited_message", "callback_query", "channel_post", "edited_channel_post"}
	for _, req := range requiredTypes {
		found := false
		for _, u := range DefaultAllowedUpdates {
			if u == req {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultAllowedUpdates missing required update type: %s", req)
		}
	}
}

func TestWebhookGuard_IsWebhookConflictError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "Nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "Standard Telegram conflict description",
			err:      fmt.Errorf("Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first"),
			expected: true,
		},
		{
			name:     "Case insensitive 409 webhook conflict",
			err:      fmt.Errorf("409 Conflict: Webhook is active"),
			expected: true,
		},
		{
			name:     "tgbotapi.Error with 409 code",
			err:      tgbotapi.Error{Code: 409, Message: "can't use getUpdates while webhook is active"},
			expected: true,
		},
		{
			name:     "400 Bad Request",
			err:      fmt.Errorf("Bad Request: message is too long"),
			expected: false,
		},
		{
			name:     "500 Internal Server Error",
			err:      fmt.Errorf("Internal Server Error"),
			expected: false,
		},
		{
			name:     "Conflict without webhook (e.g. terminated by other getUpdates)",
			err:      fmt.Errorf("Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := isWebhookConflictError(tc.err)
			if actual != tc.expected {
				t.Errorf("isWebhookConflictError(%v) = %v; want %v", tc.err, actual, tc.expected)
			}
		})
	}
}

func TestWebhookGuard_ClearWebhookOnStartup(t *testing.T) {
	var deleteCalled int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"username":"GuardBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "deleteWebhook") {
			atomic.AddInt32(&deleteCalled, 1)
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	if err := clearWebhookOnStartup(bot); err != nil {
		t.Fatalf("clearWebhookOnStartup failed: %v", err)
	}

	if atomic.LoadInt32(&deleteCalled) != 1 {
		t.Errorf("Expected deleteWebhook to be called once, got %d", atomic.LoadInt32(&deleteCalled))
	}
}

func TestWebhookGuard_ClearWebhookOnStartup_NilBot(t *testing.T) {
	if err := clearWebhookOnStartup(nil); err == nil {
		t.Errorf("Expected error for nil bot, got nil")
	}
}

func TestWebhookGuard_AutoRecovery_EndToEnd(t *testing.T) {
	var (
		mu           sync.Mutex
		getUpdatesN  int
		deleteCalled int
		alertSent    bool
		alertContent string
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		bodyBytes, _ := io.ReadAll(r.Body)

		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":777,"is_bot":true,"username":"RecoveryBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "getWebhookInfo") {
			w.Write([]byte(`{"ok":true,"result":{"url":"https://tele.goldenherd.com/tg/webhook/777","pending_update_count":0}}`))
			return
		}

		if strings.Contains(r.URL.Path, "deleteWebhook") {
			mu.Lock()
			deleteCalled++
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}

		if strings.Contains(r.URL.Path, "sendMessage") {
			mu.Lock()
			alertSent = true
			alertContent = string(bodyBytes)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{"message_id":999,"chat":{"id":12345},"text":"alert"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "getUpdates") {
			mu.Lock()
			getUpdatesN++
			n := getUpdatesN
			mu.Unlock()

			if n == 1 {
				// 1st request: simulate external webhook hijack conflict
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first"}`))
				return
			}

			// 2nd request: auto-recovery succeeded, deliver valid update
			updatePayload := map[string]interface{}{
				"ok": true,
				"result": []map[string]interface{}{
					{
						"update_id": 5001,
						"message": map[string]interface{}{
							"message_id": 1,
							"chat":       map[string]interface{}{"id": 12345},
							"from":       map[string]interface{}{"id": 12345},
							"text":       "Recovery verified!",
						},
					},
				},
			}
			json.NewEncoder(w).Encode(updatePayload)
			return
		}

		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	stopChan := make(chan struct{})
	defer close(stopChan)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 1
	u.AllowedUpdates = DefaultAllowedUpdates

	allowedAdmins := map[int64]bool{12345: true}
	updatesChan := getUpdatesWithRecovery(bot, u, allowedAdmins, stopChan)

	select {
	case update, ok := <-updatesChan:
		if !ok {
			t.Fatal("updates channel closed unexpectedly")
		}
		if update.UpdateID != 5001 {
			t.Errorf("Expected update ID 5001, got %d", update.UpdateID)
		}
		if update.Message == nil || update.Message.Text != "Recovery verified!" {
			t.Errorf("Unexpected message text: %v", update.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for update after auto-recovery")
	}

	mu.Lock()
	defer mu.Unlock()

	if deleteCalled == 0 {
		t.Errorf("Expected deleteWebhook to be called during auto-recovery, got %d calls", deleteCalled)
	}
	if !alertSent {
		t.Errorf("Expected admin alert to be sent via sendMessage upon recovery")
	}
	if !strings.Contains(alertContent, "goldenherd.com") {
		t.Errorf("Expected admin alert to mention parasite URL 'goldenherd.com', got body: %s", alertContent)
	}
}

func TestWebhookGuard_AdminAlertCooldown(t *testing.T) {
	var alertCount int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":888,"is_bot":true,"username":"CooldownBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getWebhookInfo") {
			w.Write([]byte(`{"ok":true,"result":{"url":"https://evil.com/webhook"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "deleteWebhook") {
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			atomic.AddInt32(&alertCount, 1)
			w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	admins := map[int64]bool{12345: true}

	// 1st recovery should trigger alert
	_, err = recoverFromWebhookConflict(bot, admins)
	if err != nil {
		t.Fatalf("First recovery failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("Expected 1 alert after first recovery, got %d", atomic.LoadInt32(&alertCount))
	}

	// Immediate 2nd recovery within cooldown window should NOT trigger another alert
	_, err = recoverFromWebhookConflict(bot, admins)
	if err != nil {
		t.Fatalf("Second recovery failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Errorf("Expected still 1 alert due to cooldown, got %d", atomic.LoadInt32(&alertCount))
	}
}

func TestWebhookGuard_StopChanGracefulTermination(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"username":"StopBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getUpdates") {
			// Sleep briefly to simulate long polling
			time.Sleep(100 * time.Millisecond)
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	stopChan := make(chan struct{})
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 1

	updatesChan := getUpdatesWithRecovery(bot, u, nil, stopChan)

	// Close stopChan after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stopChan)
	}()

	// The updates channel should close promptly
	select {
	case _, ok := <-updatesChan:
		if ok {
			// Drain remaining if any, but must eventually close
			for range updatesChan {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("getUpdatesWithRecovery failed to terminate promptly after stopChan closed")
	}
}
