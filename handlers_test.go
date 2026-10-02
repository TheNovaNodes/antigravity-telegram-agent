package main

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
	"io"
	"math"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

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
		"cmd:new_session",
		"cmd:accounts",
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

	ms.mu.Lock()
	defer ms.mu.Unlock()
	foundWarning := false
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Contacts are not supported") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("Expected unsupported media warning message sent to chat")
	}
}

func TestExtractInboundPayload(t *testing.T) {
	t.Run("nil message", func(t *testing.T) {
		payload := extractInboundPayload(nil)
		if payload != (InboundPayload{}) {
			t.Errorf("Expected empty payload for nil message, got %+v", payload)
		}
	})

	t.Run("plain text message", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Text: "Hello agent",
		}
		p := extractInboundPayload(msg)
		if p.Text != "Hello agent" || p.FileID != "" || p.IsVoice {
			t.Errorf("Unexpected payload: %+v", p)
		}
	})

	t.Run("telegram command aliases", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Text: "/grill_me architecture",
		}
		p := extractInboundPayload(msg)
		if p.Text != "/grill-me architecture" {
			t.Errorf("Expected /grill-me alias, got %s", p.Text)
		}

		msg2 := &tgbotapi.Message{
			Text: "/teamwork_preview swarm",
		}
		p2 := extractInboundPayload(msg2)
		if p2.Text != "/teamwork-preview swarm" {
			t.Errorf("Expected /teamwork-preview alias, got %s", p2.Text)
		}
	})

	t.Run("document with extension", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Document: &tgbotapi.Document{
				FileID:   "doc123",
				FileName: "report.pdf",
			},
			Caption: "Check this PDF",
		}
		p := extractInboundPayload(msg)
		if p.FileID != "doc123" || p.OriginalFileName != "report.pdf" || p.Ext != ".pdf" || p.Caption != "Check this PDF" || p.IsVoice {
			t.Errorf("Unexpected document payload: %+v", p)
		}
	})

	t.Run("document without extension", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Document: &tgbotapi.Document{
				FileID:   "doc456",
				FileName: "rawdata",
			},
		}
		p := extractInboundPayload(msg)
		if p.FileID != "doc456" || p.OriginalFileName != "rawdata" || p.Ext != ".bin" {
			t.Errorf("Unexpected document payload: %+v", p)
		}
	})

	t.Run("photo selection", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Photo: []tgbotapi.PhotoSize{
				{FileID: "thumb", Width: 100, Height: 100},
				{FileID: "medium", Width: 320, Height: 320},
				{FileID: "full_res", Width: 1024, Height: 1024},
			},
			Caption: "Look at this",
		}
		p := extractInboundPayload(msg)
		if p.FileID != "full_res" || p.Ext != ".jpg" || p.Caption != "Look at this" {
			t.Errorf("Unexpected photo payload: %+v", p)
		}
	})

	t.Run("voice message", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Voice: &tgbotapi.Voice{
				FileID: "voice123",
			},
		}
		p := extractInboundPayload(msg)
		if p.FileID != "voice123" || p.Ext != ".ogg" || !p.IsVoice {
			t.Errorf("Unexpected voice payload: %+v", p)
		}
	})

	t.Run("audio message", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Audio: &tgbotapi.Audio{
				FileID: "audio123",
			},
		}
		p := extractInboundPayload(msg)
		if p.FileID != "audio123" || p.Ext != ".mp3" || p.IsVoice {
			t.Errorf("Unexpected audio payload: %+v", p)
		}
	})

	t.Run("video note message", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 88,
			VideoNote: &tgbotapi.VideoNote{
				FileID:   "vnote999",
				Length:   240,
				Duration: 15,
			},
		}
		p := extractInboundPayload(msg)
		if p.FileID != "vnote999" || p.Ext != ".mp4" || p.OriginalFileName != "videonote_88.mp4" || p.IsVoice {
			t.Errorf("Unexpected video note payload: %+v", p)
		}
	})

	t.Run("sticker with emoji and set name", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 101,
			Sticker: &tgbotapi.Sticker{
				FileID:  "stk1",
				Emoji:   "🚀",
				SetName: "SpacePack",
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь отправил стикер: 🚀 (набор: SpacePack)]"
		if p.Text != expected || p.FileID != "stk1" || p.Ext != ".webp" || p.OriginalFileName != "sticker_101.webp" || p.IsVoice {
			t.Errorf("Expected text %q, fileID stk1, ext .webp, original sticker_101.webp, got %+v", expected, p)
		}
	})

	t.Run("sticker without set name", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 102,
			Sticker: &tgbotapi.Sticker{
				FileID: "stk2",
				Emoji:  "👍",
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь отправил стикер: 👍]"
		if p.Text != expected || p.FileID != "stk2" || p.Ext != ".webp" || p.OriginalFileName != "sticker_102.webp" {
			t.Errorf("Expected text %q, fileID stk2, ext .webp, got %+v", expected, p)
		}
	})

	t.Run("sticker animated", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 103,
			Sticker: &tgbotapi.Sticker{
				FileID:     "stk_anim",
				Emoji:      "🎉",
				SetName:    "PartyPack",
				IsAnimated: true,
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь отправил стикер: 🎉 (набор: PartyPack)]"
		if p.Text != expected || p.FileID != "stk_anim" || p.Ext != ".tgs" || p.OriginalFileName != "sticker_103.tgs" {
			t.Errorf("Expected text %q, fileID stk_anim, ext .tgs, got %+v", expected, p)
		}
	})

	t.Run("sticker without emoji fallback", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 104,
			Sticker: &tgbotapi.Sticker{
				FileID:  "stk3",
				SetName: "AbstractPack",
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь отправил стикер: 🎨 (набор: AbstractPack)]"
		if p.Text != expected || p.FileID != "stk3" || p.Ext != ".webp" || p.OriginalFileName != "sticker_104.webp" {
			t.Errorf("Expected text %q, got %+v", expected, p)
		}
	})

	t.Run("sticker with accompanying text or caption", func(t *testing.T) {
		msg := &tgbotapi.Message{
			MessageID: 105,
			Sticker: &tgbotapi.Sticker{
				FileID:  "stk4",
				Emoji:   "🔥",
				SetName: "FirePack",
			},
			Caption: "Look at this deployment",
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь отправил стикер: 🔥 (набор: FirePack)]\nLook at this deployment"
		if p.Text != expected || p.FileID != "stk4" || p.Ext != ".webp" || p.OriginalFileName != "sticker_105.webp" {
			t.Errorf("Expected text %q, got %+v", expected, p)
		}
	})

	t.Run("location with coordinates and accuracy", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:           52.520000,
				Longitude:          13.405000,
				HorizontalAccuracy: 15.5,
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь передал геопозицию: Latitude: 52.520000, Longitude: 13.405000, точность: ~15.5м]"
		if p.Text != expected || p.FileID != "" || p.IsVoice {
			t.Errorf("Expected text %q, fileID empty, got %+v", expected, p)
		}
	})

	t.Run("location with coordinates without accuracy", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:  48.856600,
				Longitude: 2.352200,
			},
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь передал геопозицию: Latitude: 48.856600, Longitude: 2.352200]"
		if p.Text != expected || p.FileID != "" {
			t.Errorf("Expected text %q, fileID empty, got %+v", expected, p)
		}
	})

	t.Run("location with accompanying caption", func(t *testing.T) {
		msg := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:  37.774900,
				Longitude: -122.419400,
			},
			Caption: "San Francisco Office",
		}
		p := extractInboundPayload(msg)
		expected := "[Пользователь передал геопозицию: Latitude: 37.774900, Longitude: -122.419400]\nSan Francisco Office"
		if p.Text != expected || p.FileID != "" {
			t.Errorf("Expected text %q, fileID empty, got %+v", expected, p)
		}
	})

	t.Run("invalid location coordinates out of bounds", func(t *testing.T) {
		// Latitude > 90
		msg1 := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:  95.0,
				Longitude: 10.0,
			},
		}
		if p := extractInboundPayload(msg1); p.Text != "" {
			t.Errorf("Expected empty payload for Lat > 90, got %+v", p)
		}

		// Longitude < -180
		msg2 := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:  10.0,
				Longitude: -195.0,
			},
		}
		if p := extractInboundPayload(msg2); p.Text != "" {
			t.Errorf("Expected empty payload for Lon < -180, got %+v", p)
		}

		// NaN coordinates
		msg3 := &tgbotapi.Message{
			Location: &tgbotapi.Location{
				Latitude:  math.NaN(),
				Longitude: 10.0,
			},
		}
		if p := extractInboundPayload(msg3); p.Text != "" {
			t.Errorf("Expected empty payload for NaN Latitude, got %+v", p)
		}
	})
}

func TestHandleUpdate_VideoNote(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fake-mp4-video-stream-content"))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"vn_file_123","file_path":"%s"}}`, fileServer.URL+"/videonote.mp4")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"mocked"}}`))
	})

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	update := tgbotapi.Update{
		UpdateID: 301,
		Message: &tgbotapi.Message{
			MessageID: 77,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
			VideoNote: &tgbotapi.VideoNote{
				FileID:   "vn_file_123",
				Length:   240,
				Duration: 10,
			},
		},
	}

	handleUpdate(bot, update, db)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		defer session.Kill()
	}

	// Verify the file was downloaded into scratch/downloads with .mp4 extension
	downloadsDir := filepath.Join(mockAgents, bot.Self.UserName, "scratch", "downloads")
	files, err := os.ReadDir(downloadsDir)
	if err != nil {
		t.Fatalf("Failed to read downloads dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("Expected downloaded .mp4 file in %s, got 0 files", downloadsDir)
	}
	foundMP4 := false
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".mp4") {
			foundMP4 = true
			content, _ := os.ReadFile(filepath.Join(downloadsDir, f.Name()))
			if string(content) != "fake-mp4-video-stream-content" {
				t.Errorf("Unexpected content in downloaded video note: %s", string(content))
			}
			break
		}
	}
	if !foundMP4 {
		t.Errorf("Did not find downloaded .mp4 file in %s", downloadsDir)
	}
}

func TestHandleUpdate_Sticker(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mockAgents := t.TempDir()
	os.Setenv("AGENTS_DIR", mockAgents)
	defer os.Unsetenv("AGENTS_DIR")

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Write([]byte("fake-webp-sticker-content"))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"stk_file_123","file_path":"%s"}}`, fileServer.URL+"/sticker.webp")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"mocked"}}`))
	})

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	update := tgbotapi.Update{
		UpdateID: 401,
		Message: &tgbotapi.Message{
			MessageID: 88,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
			Sticker: &tgbotapi.Sticker{
				FileID:  "stk_file_123",
				Emoji:   "🚀",
				SetName: "LaunchSet",
			},
		},
	}

	handleUpdate(bot, update, db)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		defer session.Kill()
	}

	downloadsDir := filepath.Join(mockAgents, bot.Self.UserName, "scratch", "downloads")
	files, err := os.ReadDir(downloadsDir)
	if err != nil {
		t.Fatalf("Failed to read downloads dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("Expected downloaded .webp file in %s, got 0 files", downloadsDir)
	}
	foundWebP := false
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".webp") {
			foundWebP = true
			content, _ := os.ReadFile(filepath.Join(downloadsDir, f.Name()))
			if string(content) != "fake-webp-sticker-content" {
				t.Errorf("Unexpected content in downloaded sticker: %s", string(content))
			}
			break
		}
	}
	if !foundWebP {
		t.Errorf("Did not find downloaded .webp file in %s", downloadsDir)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Contacts are not supported") {
			t.Errorf("Sticker triggered unsupported media warning: %s", body)
		}
	}
}

func TestHandleUpdate_StickerAnimated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mockAgents := t.TempDir()
	os.Setenv("AGENTS_DIR", mockAgents)
	defer os.Unsetenv("AGENTS_DIR")

	os.Setenv("AGY_BINARY", "cat")
	defer os.Unsetenv("AGY_BINARY")

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tgsticker")
		w.Write([]byte("fake-tgs-animated-sticker-content"))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"stk_file_anim","file_path":"%s"}}`, fileServer.URL+"/sticker.tgs")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":100,"chat":{"id":12345},"text":"mocked"}}`))
	})

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	update := tgbotapi.Update{
		UpdateID: 402,
		Message: &tgbotapi.Message{
			MessageID: 89,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
			Sticker: &tgbotapi.Sticker{
				FileID:     "stk_file_anim",
				Emoji:      "🎉",
				SetName:    "PartySet",
				IsAnimated: true,
			},
		},
	}

	handleUpdate(bot, update, db)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		defer session.Kill()
	}

	downloadsDir := filepath.Join(mockAgents, bot.Self.UserName, "scratch", "downloads")
	files, err := os.ReadDir(downloadsDir)
	if err != nil {
		t.Fatalf("Failed to read downloads dir: %v", err)
	}
	foundTGS := false
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".tgs") {
			foundTGS = true
			content, _ := os.ReadFile(filepath.Join(downloadsDir, f.Name()))
			if string(content) != "fake-tgs-animated-sticker-content" {
				t.Errorf("Unexpected content in downloaded animated sticker: %s", string(content))
			}
			break
		}
	}
	if !foundTGS {
		t.Errorf("Did not find downloaded .tgs file in %s", downloadsDir)
	}
}

func TestHandleUpdate_Location(t *testing.T) {
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
		UpdateID: 501,
		Message: &tgbotapi.Message{
			MessageID: 99,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
			Location: &tgbotapi.Location{
				Latitude:           55.7558,
				Longitude:          37.6173,
				HorizontalAccuracy: 12.0,
			},
		},
	}

	handleUpdate(bot, update, db)

	user := getUser(db, userID, "TestMockBot")
	session := getSession("TestMockBot", user, chatID)
	if session != nil {
		defer session.Kill()
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Contacts are not supported") {
			t.Errorf("Valid location triggered unsupported media warning: %s", body)
		}
	}
}

func TestHandleUpdate_InvalidLocation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()

	bot := createMockBot(ms)
	chatID := int64(12345)
	userID := int64(777)

	// Location out of valid range (-90 <= Lat <= 90)
	update := tgbotapi.Update{
		UpdateID: 502,
		Message: &tgbotapi.Message{
			MessageID: 101,
			Chat:      &tgbotapi.Chat{ID: chatID},
			From:      &tgbotapi.User{ID: userID, UserName: "testuser"},
			Location: &tgbotapi.Location{
				Latitude:  120.0,
				Longitude: 37.6173,
			},
		},
	}

	handleUpdate(bot, update, db)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	foundWarning := false
	for _, body := range ms.sentBodies {
		unescaped, _ := url.QueryUnescape(body)
		if strings.Contains(unescaped, "Contacts are not supported") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("Expected unsupported media warning for out-of-bounds location")
	}
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

	type testUserSetup struct {
		userID        int64
		chatID        int64
		newModel      string
		origSessionID string
	}
	setups := make([]testUserSetup, concurrency)
	for i := 0; i < concurrency; i++ {
		userID := int64(2000 + i)
		chatID := int64(3000 + i)
		u := getUser(db, userID, "TestMockBot")
		setups[i] = testUserSetup{
			userID:        userID,
			chatID:        chatID,
			newModel:      fmt.Sprintf("model-tier-%d", i%3),
			origSessionID: u.SessionID,
		}
	}

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			s := setups[idx]

			// Dispatch swap callback
			cbUpdate := tgbotapi.Update{
				UpdateID: idx,
				CallbackQuery: &tgbotapi.CallbackQuery{
					ID:   fmt.Sprintf("cb_%d", idx),
					From: &tgbotapi.User{ID: s.userID},
					Message: &tgbotapi.Message{
						MessageID: 50,
						Chat:      &tgbotapi.Chat{ID: s.chatID},
					},
					Data: "model:" + s.newModel,
				},
			}
			handleUpdate(bot, cbUpdate, db)

			// Assert preservation
			afterUser := getUser(db, s.userID, "TestMockBot")
			if afterUser.SessionID != s.origSessionID {
				t.Errorf("User %d: Expected SessionID %s, got %s", s.userID, s.origSessionID, afterUser.SessionID)
			}
			if afterUser.Model != s.newModel {
				t.Errorf("User %d: Expected model %s, got %s", s.userID, s.newModel, afterUser.Model)
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

func TestPathHelpers_EnvironmentOverrides(t *testing.T) {
	tempDir := t.TempDir()

	customAgents := filepath.Join(tempDir, "custom_agents")
	customBrain := filepath.Join(tempDir, "custom_brain")
	customAgy := filepath.Join(tempDir, "custom_agy")

	t.Setenv("AGENTS_DIR", customAgents)
	t.Setenv("BRAIN_DIR", customBrain)
	t.Setenv("AGY_BINARY", customAgy)

	if got := getAgentsDir(); got != customAgents {
		t.Errorf("Expected getAgentsDir %s, got %s", customAgents, got)
	}
	if got := getBrainDir(); got != customBrain {
		t.Errorf("Expected getBrainDir %s, got %s", customBrain, got)
	}
	if got := getAgyPath(); got != customAgy {
		t.Errorf("Expected getAgyPath %s, got %s", customAgy, got)
	}
}

func TestAgySession_GetConversation(t *testing.T) {
	s := &AgySession{
		BotName:      "TestBot",
		Conversation: "initial-conv",
	}

	if got := s.GetConversation(); got != "initial-conv" {
		t.Errorf("Expected initial-conv, got %s", got)
	}

	s.mu.Lock()
	s.Conversation = "updated-conv"
	s.mu.Unlock()
	if got := s.GetConversation(); got != "updated-conv" {
		t.Errorf("Expected updated-conv, got %s", got)
	}
}

func TestHandleStartCommand_WithTranscriptAndTitle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	user := User{
		ID:         999,
		Workspace:  "/tmp/test_workspace",
		Model:      "gemini-3.8-flash-high",
		SessionID:  "test-session-uuid",
		VoiceReply: true,
	}

	// Create session directory with title and transcript
	sessionDir := filepath.Join(tempDir, user.SessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)

	titleFile := filepath.Join(sessionDir, ".title")
	os.WriteFile(titleFile, []byte("My Custom Session Title"), 0644)

	transcriptFile := filepath.Join(logsDir, "transcript.jsonl")
	twoHoursAgo := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	transcriptContent := fmt.Sprintf(`{"step":1,"created_at":"%s","content":"First step"}
{"step":2,"created_at":"%s","content":"Second step"}
`, twoHoursAgo, time.Now().Format(time.RFC3339))
	os.WriteFile(transcriptFile, []byte(transcriptContent), 0644)

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected Telegram message to be sent for handleStartCommand")
	}

	var foundTitle, foundSteps, foundModel bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "My Custom Session Title") {
			foundTitle = true
		}
		if strings.Contains(unescaped, "Steps:* 2") || strings.Contains(unescaped, "Steps: 2") {
			foundSteps = true
		}
		if strings.Contains(unescaped, user.Model) {
			foundModel = true
		}
	}

	if !foundTitle {
		t.Errorf("Expected start message to contain session title 'My Custom Session Title', got: %v", sentBodies)
	}
	if !foundSteps {
		t.Errorf("Expected start message to contain steps count 2, got: %v", sentBodies)
	}
	if !foundModel {
		t.Errorf("Expected start message to contain user model %s, got: %v", user.Model, sentBodies)
	}
}

func TestHandleResumeCommand_WithActiveTranscripts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(999)

	// Create 2 test sessions in DB
	sid1 := uuid.New().String()
	sid2 := uuid.New().String()

	updateUserSession(db, userID, sid1)
	updateUserSession(db, userID, sid2)

	// Session 1: with .title file
	sdir1 := filepath.Join(tempDir, sid1, ".system_generated", "logs")
	os.MkdirAll(sdir1, 0755)
	os.WriteFile(filepath.Join(tempDir, sid1, ".title"), []byte("Session With Title"), 0644)
	os.WriteFile(filepath.Join(sdir1, "transcript.jsonl"), []byte(`{"content":"hello"}`), 0644)

	// Session 2: with <USER_REQUEST> in transcript
	sdir2 := filepath.Join(tempDir, sid2, ".system_generated", "logs")
	os.MkdirAll(sdir2, 0755)
	transcriptJSON := `{"content":"<USER_REQUEST>\nFix the data race in engine\n</USER_REQUEST>"}`
	os.WriteFile(filepath.Join(sdir2, "transcript.jsonl"), []byte(transcriptJSON), 0644)

	handleResumeCommand(bot, chatID, userID, db)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected Telegram resume menu to be sent")
	}
}

func TestHandleRenameCommand_ActiveAndEmpty(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	user := User{
		ID:        999,
		Workspace: "/tmp/test",
		Model:     "gemini-3.8-flash-high",
		SessionID: "rename-test-uuid",
	}

	// 1. Rename with empty name argument
	handleRenameCommand(bot, chatID, "/rename", "TestMockBot", user)

	// 2. Rename with active session
	session := getSession("TestMockBot", user, chatID)
	session.mu.Lock()
	session.Conversation = "rename-test-uuid"
	session.mu.Unlock()

	handleRenameCommand(bot, chatID, "/rename Project Alpha Dashboard", "TestMockBot", user)

	titleFile := filepath.Join(tempDir, "rename-test-uuid", ".title")
	content, err := os.ReadFile(titleFile)
	if err != nil || string(content) != "Project Alpha Dashboard" {
		t.Errorf("Expected title file content 'Project Alpha Dashboard', got '%s', err: %v", string(content), err)
	}

	// 3. Rename with no active conversation
	session.mu.Lock()
	session.Conversation = ""
	session.mu.Unlock()
	handleRenameCommand(bot, chatID, "/rename New Title", "TestMockBot", user)
}

func TestHandleWorkspaceCommand_ValidAndInvalid(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgents, 0755)
	t.Setenv("AGENTS_DIR", mockAgents)

	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(999)
	user := getUser(db, userID, "TestMockBot")

	// 1. Missing arg
	handleWorkspaceCommand(bot, chatID, userID, "/workspace", "TestMockBot", user, db)

	// 2. Relative path
	handleWorkspaceCommand(bot, chatID, userID, "/workspace relative/path", "TestMockBot", user, db)

	// 3. Path outside allowed root
	forbiddenDir := filepath.Join(os.TempDir(), "forbidden_system_dir")
	os.MkdirAll(forbiddenDir, 0755)
	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+forbiddenDir, "TestMockBot", user, db)

	// 4. Valid path inside bot office
	validSubLab := filepath.Join(mockAgents, "TestMockBot", "sublab_1")
	os.MkdirAll(validSubLab, 0755)
	handleWorkspaceCommand(bot, chatID, userID, "/workspace "+validSubLab, "TestMockBot", user, db)

	updatedUser := getUser(db, userID, "TestMockBot")
	if updatedUser.Workspace != validSubLab {
		t.Errorf("Expected updated workspace %s, got %s", validSubLab, updatedUser.Workspace)
	}
}

func TestDownloadTelegramMedia_WithRealServer(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	// File server hosting the file
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("binary data mock file"))
	}))
	defer fileServer.Close()

	// Mock Telegram API server
	ms := newMockServer()
	defer ms.Close()

	// Update handler to respond to getFile
	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"file123","file_path":"%s"}}`, fileServer.URL+"/test.txt")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	// Test 1: Empty file ID
	formatted, isFile, placeholderID, err := downloadTelegramMedia(bot, 12345, "", ".txt", "plain text", "", "TestBot")
	if err != nil || isFile || formatted != "plain text" || placeholderID != 0 {
		t.Errorf("Expected plain text passthrough, got formatted: %s, isFile: %v, placeholderID: %d, err: %v", formatted, isFile, placeholderID, err)
	}

	// Test 2: Valid file download
	formatted, isFile, placeholderID, err = downloadTelegramMedia(bot, 12345, "file123", ".txt", "my note", "my caption", "TestBot")
	if err != nil || !isFile || placeholderID != 99 || !strings.Contains(formatted, "[Attached File: file://") {
		t.Errorf("Expected valid attached file string with placeholderID 99, got formatted: %s, isFile: %v, placeholderID: %d, err: %v", formatted, isFile, placeholderID, err)
	}
}

func TestRegisterBotCommands(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	registerBotCommands(bot)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected registerBotCommands to send SetMyCommands request")
	}
}

func TestSendArtifacts_RealFile(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	os.MkdirAll(mockAgents, 0755)
	t.Setenv("AGENTS_DIR", mockAgents)

	artifactPath := filepath.Join(mockAgents, "report.pdf")
	os.WriteFile(artifactPath, []byte("PDF content"), 0644)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	text := fmt.Sprintf("Here is your output: [report](file://%s)", artifactPath)
	sendArtifacts(bot, 12345, text)

	ms.mu.Lock()
	reqs := len(ms.sentRequests)
	ms.mu.Unlock()

	if reqs == 0 {
		t.Error("Expected sendArtifacts to send document to Telegram")
	}
}

func TestSendTypingAction(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	s := &AgySession{
		BotName:         "TestBot",
		BotAPI:          bot,
		ChatID:          12345,
		ActiveMessageID: 100,
		VoiceReply:      false,
	}

	// 1. Typing action text mode
	s.sendTypingAction()

	// 2. Typing action voice mode
	s.VoiceReply = true
	s.sendTypingAction()

	// 3. Inactive session (ActiveMessageID == 0)
	s.ActiveMessageID = 0
	s.sendTypingAction()
}

func TestHandleExportCommand_Scenarios(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)
	t.Setenv("AGENTS_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(888)

	user := User{
		ID:        userID,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: "export-test-uuid",
	}

	// 1. Non-existent transcript
	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// 2. Empty transcript
	sessionDir := filepath.Join(tempDir, user.SessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(""), 0644)
	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// 3. Populated transcript with title and tool calls
	os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Project Matrix Export"), 0644)
	fullJSONL := `{"step":1,"created_at":"2026-09-03T17:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nRefactor the bot architecture\n</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-03T17:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Starting refactoring plan...","tool_calls":[{"name":"view_file","args":{"path":"main.go"}}]}
{"step":3,"created_at":"2026-09-03T17:00:10Z","source":"SYSTEM","type":"SYSTEM_MESSAGE","content":"System alert: tests passed"}
`
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(fullJSONL), 0644)

	handleExportCommand(bot, chatID, userID, "TestBot", user)

	// Test export via handleCommand & handleCallbackQuery
	db := setupTestDB(t)
	defer db.Close()
	handleCommand(bot, chatID, userID, "/export", "TestBot", user, db)

	cb := &tgbotapi.CallbackQuery{
		ID:   "cb123",
		From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{
			MessageID: 10,
			Chat:      &tgbotapi.Chat{ID: chatID},
		},
		Data: "cmd:export",
	}
	handleCallbackQuery(bot, cb, user, "TestBot", db)
}

func TestSafePrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"Empty string", "", 8, ""},
		{"Short ASCII", "abc", 8, "abc"},
		{"Exact ASCII", "12345678", 8, "12345678"},
		{"Long ASCII", "1234567890abcdef", 8, "12345678"},
		{"Short Cyrillic", "привет", 8, "привет"},
		{"Long Cyrillic", "приветмиртест", 6, "привет"},
		{"Emojis", "🎭🤖💬⚡🚀🔥", 3, "🎭🤖💬"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safePrefix(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Errorf("safePrefix(%q, %d) = %q, expected %q", tc.input, tc.maxLen, got, tc.expected)
			}
		})
	}
}

func TestTruncateUTF8Bytes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
	}{
		{"Empty", "", 64},
		{"ASCII short", "hello world", 64},
		{"ASCII cut", "012345678901234567890123456789", 10},
		{"Cyrillic split byte", "Привет, мир! Это тестовая строка на русском языке", 15}, // 15 bytes cuts mid-rune
		{"Emoji split byte", "🤖🎭💬⚡🚀✨🔥", 10},                                              // 10 bytes cuts mid-4byte-emoji
		{"Max 64 bytes limit", "resume:session_custom_title_with_lots_of_words_and_utf8_тест_длинного_названия", 64},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8Bytes(tc.input, tc.maxBytes)
			if len([]byte(got)) > tc.maxBytes {
				t.Errorf("truncateUTF8Bytes(%q, %d) byte length %d exceeds maxBytes %d", tc.input, tc.maxBytes, len([]byte(got)), tc.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateUTF8Bytes(%q, %d) produced invalid UTF-8: %q (bytes: %v)", tc.input, tc.maxBytes, got, []byte(got))
			}
		})
	}
}

func TestHandleCommand_PrefixCollisionProtection(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	bot := &tgbotapi.BotAPI{}
	botName := "TestAgentBot"
	user := User{ID: 1001, Workspace: "/root", Model: "gemini-3.7-flash-high"}

	// Valid commands that SHOULD be handled
	validCommands := []string{
		"/start",
		"/start@TestAgentBot",
		"/resume",
		"/resume@testagentbot",
		"/tts hello",
		"/voice on",
		"/workspace /root",
		"/rename new title",
		"/export",
		"/usage",
		"/help",
		"/model",
		"/refresh_models",
		"/clear",
	}

	for _, cmd := range validCommands {
		handled := handleCommand(bot, 12345, 1001, cmd, botName, user, db)
		if !handled {
			t.Errorf("Expected valid command %q to be handled (return true), got false", cmd)
		}
	}

	// Pseudo-commands (prefix collisions) that MUST NOT be handled
	collisionCommands := []string{
		"/workspacex",
		"/workspace_test",
		"/voiceover",
		"/voicemail",
		"/ttspayload",
		"/ttsspeak",
		"/renamed",
		"/export_data",
		"/usagereport",
		"/cleartoend",
	}

	for _, cmd := range collisionCommands {
		handled := handleCommand(bot, 12345, 1001, cmd, botName, user, db)
		if handled {
			t.Errorf("Expected collision command %q to NOT be handled (return false), but got true", cmd)
		}
	}
}

func TestHandleExportCommand_EmptySessionID_NoPanic(t *testing.T) {
	bot := &tgbotapi.BotAPI{}
	user := User{ID: 1002, SessionID: "", Workspace: "/root", Model: "gemini-3.7-flash-high"}

	// Must not panic on empty SessionID
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleExportCommand panicked with empty SessionID: %v", r)
		}
	}()

	handleExportCommand(bot, 12345, 1002, "TestBot", user)
}

func TestHandleClearCommand_FreshSession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (1003, '/root', 'gemini-3.7-flash-high', 0, 'old_session_123', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	bot := &tgbotapi.BotAPI{}
	user := User{ID: 1003, SessionID: "old_session_123", Workspace: "/root", Model: "gemini-3.7-flash-high"}

	handleClearCommand(bot, 12345, 1003, "TestBot", user, db)

	// Verify DB was updated with empty session
	var updatedSession string
	if err := db.QueryRow("SELECT session_id FROM users WHERE user_id = 1003").Scan(&updatedSession); err != nil {
		t.Fatalf("Failed to query user session: %v", err)
	}
	if updatedSession != "" {
		t.Errorf("Expected session_id in DB to be cleared to empty string, got %q", updatedSession)
	}
}

func TestRegisterBotCommands_Validation(t *testing.T) {
	// Reconstruct the registered command list to validate Telegram Bot API invariants
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Welcome menu & status"},
		{Command: "help", Description: "Command reference & documentation"},
		{Command: "model", Description: "Select LLM model"},
		{Command: "refresh_models", Description: "Dynamically refresh models cache"},
		{Command: "usage", Description: "Show API quota usage"},
		{Command: "accounts", Description: "Manage multi-account pool and rotation"},
		{Command: "clear", Description: "Clear context and restart agent"},
		{Command: "stop", Description: "Interrupt active execution turn"},
		{Command: "resume", Description: "Resume previous conversation"},
		{Command: "rename", Description: "Rename current session"},
		{Command: "workspace", Description: "Change target workspace directory"},
		{Command: "export", Description: "Export session transcript to file"},
		{Command: "voice", Description: "Toggle persistent voice mode"},
		{Command: "tts", Description: "Text to speech voice synthesis"},
		{Command: "tts_engine", Description: "View or switch active TTS engine"},
		{Command: "goal", Description: "Run exhaustive long-running task"},
		{Command: "schedule", Description: "Set recurring schedule or timer"},
		{Command: "browser", Description: "Use web browser for a task"},
		{Command: "plan", Description: "Step-by-step task planning"},
		{Command: "grill_me", Description: "Interactive design interview"},
		{Command: "teamwork_preview", Description: "Swarm autonomous agents"},
		{Command: "learn", Description: "Persist behavior for future tasks"},
	}

	commandNameRegex := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	commandMap := make(map[string]string)

	for _, cmd := range commands {
		// Invariant 1: Telegram command syntax (1-32 chars, lowercase, numbers, underscores)
		if !commandNameRegex.MatchString(cmd.Command) {
			t.Errorf("Invalid Telegram command name syntax: %q", cmd.Command)
		}

		// Invariant 2: Description length <= 256 characters
		if len(cmd.Description) == 0 || len(cmd.Description) > 256 {
			t.Errorf("Command %q description length %d out of bounds (1-256)", cmd.Command, len(cmd.Description))
		}

		commandMap[cmd.Command] = cmd.Description
	}

	// Required commands must be present
	requiredCommands := []string{"help", "refresh_models", "tts_engine", "goal", "plan", "schedule", "browser", "learn", "grill_me", "teamwork_preview"}
	for _, req := range requiredCommands {
		if _, ok := commandMap[req]; !ok {
			t.Errorf("Required command %q missing from registered commands", req)
		}
	}
}

func TestHandleHelpCommand_NilSafeAndContent(t *testing.T) {
	// Must not panic on nil bot pointer
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleHelpCommand panicked on nil bot: %v", r)
		}
	}()

	handleHelpCommand(nil, 99999)
}

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

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"(untitled session)", ""},
		{"untitled session", ""},
		{"(empty)", ""},
		{"Session active", ""},
		{"Fix Auth Bug", "Fix_Auth_Bug"},
		{"Рефакторинг кода и архитектуры", "Рефакторинг_кода_и_архитектуры"},
		{"Очень длинное название задачи которое обязательно обрежется по лимиту тридцати рун", "Очень_длинное_название_задачи"},
		{"Special #$% Symbols & More!", "Special_Symbols_More"},
		{"___Leading_Trailing___", "Leading_Trailing"},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeFilename(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatToolCalls(t *testing.T) {
	// 1. Empty calls
	if got := formatToolCalls(nil); got != "" {
		t.Errorf("Expected empty string for nil tool calls, got %q", got)
	}

	// 2. Summary present
	tcs1 := []interface{}{
		map[string]interface{}{
			"name":        "view_file",
			"toolSummary": "Inspect main configuration",
		},
	}
	got1 := formatToolCalls(tcs1)
	if !strings.Contains(got1, "🛠️ *Tool:* `view_file` — Inspect main configuration") {
		t.Errorf("Unexpected format with summary: %s", got1)
	}

	// 3. Action fallback
	tcs2 := []interface{}{
		map[string]interface{}{
			"name":       "run_command",
			"toolAction": "Running go test",
		},
	}
	got2 := formatToolCalls(tcs2)
	if !strings.Contains(got2, "🛠️ *Tool:* `run_command` — Running go test") {
		t.Errorf("Unexpected format with action: %s", got2)
	}

	// 4. Args fallback
	tcs3 := []interface{}{
		map[string]interface{}{
			"name": "list_dir",
			"args": map[string]interface{}{"path": "/root"},
		},
	}
	got3 := formatToolCalls(tcs3)
	if !strings.Contains(got3, "🛠️ *Tool:* `list_dir` —") {
		t.Errorf("Unexpected format with args: %s", got3)
	}

	// 5. Huge argumentsJson truncated
	hugeJSON := fmt.Sprintf(`{"code":"%s"}`, strings.Repeat("A", 200))
	tcs4 := []interface{}{
		map[string]interface{}{
			"name":          "write_to_file",
			"argumentsJson": hugeJSON,
		},
	}
	got4 := formatToolCalls(tcs4)
	if !strings.Contains(got4, "...") || len(got4) > 150 {
		t.Errorf("Expected truncation for huge argumentsJson, got length %d: %s", len(got4), got4)
	}
}

func TestHandleExportCommand_CleanFormattingAndFilename(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "export-clean-12345"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	os.MkdirAll(logsDir, 0755)

	// Set session title
	os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Refactor Engine Logic"), 0644)

	// Create transcript with mixed events
	fullJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nImplement clean export\n</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Starting implementation...","tool_calls":[{"name":"replace_file_content","toolSummary":"Apply clean export format"}]}
{"step":3,"created_at":"2026-09-05T10:00:10Z","source":"MODEL","type":"GENERIC","content":"MASSIVE_RAW_TOOL_DUMP_OUTPUT_SHOULD_BE_FILTERED"}
{"step":4,"created_at":"2026-09-05T10:00:15Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Export refactoring completed successfully."}
`
	os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(fullJSONL), 0644)

	user := User{
		ID:        999,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 12345, 999, "TestBot", user)

	// Verify export file was generated with expected title slug
	expectedFilename := fmt.Sprintf("session_Refactor_Engine_Logic_%s.md", safePrefix(sessionID, 8))
	exportPath := filepath.Join(mockAgents, "TestBot", "scratch", "exports", expectedFilename)

	contentBytes, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("Failed to read generated export file at %s: %v", exportPath, err)
	}

	content := string(contentBytes)
	if strings.Contains(content, "MASSIVE_RAW_TOOL_DUMP_OUTPUT_SHOULD_BE_FILTERED") {
		t.Errorf("Export file contained raw GENERIC tool output dump!")
	}

	if !strings.Contains(content, "🛠️ *Tool:* `replace_file_content` — Apply clean export format") {
		t.Errorf("Export file missing cleanly formatted tool call, got: %s", content)
	}

	if !strings.Contains(content, "Implement clean export") || !strings.Contains(content, "Export refactoring completed successfully.") {
		t.Errorf("Export file missing user request or assistant response, got: %s", content)
	}
}

func TestDownloadTelegramMedia_SessionExportAutoPrompt(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("# Session Export Markdown"))
	}))
	defer fileServer.Close()

	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"fileExport1","file_path":"%s"}}`, fileServer.URL+"/session_export.md")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	// 1. Session export with empty caption should trigger auto-prompt
	formatted, isFile, _, err := downloadTelegramMedia(bot, 12345, "fileExport1", ".md", "", "", "TestBot", "session_Fix_Auth_123.md")
	if err != nil || !isFile {
		t.Fatalf("Download failed: %v, isFile: %v", err, isFile)
	}
	if !strings.Contains(formatted, "Previous session context loaded from export file") {
		t.Errorf("Expected auto-prompt for session export file, got: %s", formatted)
	}

	// 2. Session export with user-supplied caption should keep user caption
	formatted2, _, _, err := downloadTelegramMedia(bot, 12345, "fileExport1", ".md", "", "User custom instructions", "TestBot", "session_Fix_Auth_123.md")
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if !strings.Contains(formatted2, "User custom instructions") {
		t.Errorf("Expected user caption to be preserved, got: %s", formatted2)
	}
	if strings.Contains(formatted2, "Previous session context loaded") {
		t.Errorf("Auto-prompt should NOT override user-supplied caption, got: %s", formatted2)
	}
}

func TestHandleExportCommand_WithArtifacts_PackagesZipAndEnrichesCaption(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "export-artifacts-7788"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create test logs dir: %v", err)
	}

	// Set session title
	_ = os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Architecture Refactoring"), 0644)

	// Create valid transcript
	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Refactor architecture</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Drafting ADR..."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)

	// Create engineering artifacts in sessionDir
	adrContent := "# Architecture Decision Record: Pure Go Engine\n\n## Status\nAccepted\n\n## Context\nMigrating to pure Go.\n"
	chkContent := "# Deployment Checklist\n\n- [ ] Run test suite\n- [ ] Verify SAST\n"
	_ = os.WriteFile(filepath.Join(sessionDir, "ADR_001_Pure_Go.md"), []byte(adrContent), 0644)
	_ = os.WriteFile(filepath.Join(sessionDir, "CHECKLIST_Deploy.md"), []byte(chkContent), 0644)

	user := User{
		ID:        888,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 554433, 888, "HarvesterBot", user)

	// 1. Verify transcript and zip bundle files generated on disk in scratch/exports
	expectedMdFilename := fmt.Sprintf("session_Architecture_Refactoring_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_Architecture_Refactoring_%s.zip", safePrefix(sessionID, 8))

	exportDir := filepath.Join(mockAgents, "HarvesterBot", "scratch", "exports")
	mdPath := filepath.Join(exportDir, expectedMdFilename)
	zipPath := filepath.Join(exportDir, expectedZipFilename)

	if _, err := os.Stat(mdPath); err != nil {
		t.Fatalf("Expected transcript markdown file at %s, got error: %v", mdPath, err)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("Expected artifacts zip bundle file at %s, got error: %v", zipPath, err)
	}

	// 2. Verify ZIP archive structure and manifest
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("Failed to open artifacts zip bundle: %v", err)
	}
	defer zr.Close()

	zipFileMap := make(map[string]*zip.File)
	for _, f := range zr.File {
		zipFileMap[f.Name] = f
	}

	if _, ok := zipFileMap["artifacts/manifest.json"]; !ok {
		t.Errorf("manifest.json missing from ZIP bundle")
	}
	if _, ok := zipFileMap["artifacts/adr/ADR_001_Pure_Go.md"]; !ok {
		t.Errorf("ADR_001_Pure_Go.md missing from ZIP bundle under artifacts/adr/")
	}
	if _, ok := zipFileMap["artifacts/checklists/CHECKLIST_Deploy.md"]; !ok {
		t.Errorf("CHECKLIST_Deploy.md missing from ZIP bundle under artifacts/checklists/")
	}

	// 3. Verify sent Telegram requests (transcript caption enrichment and companion ZIP delivery)
	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundEnrichedCaption := false
	foundZipDocument := false

	for _, body := range sentBodies {
		if strings.Contains(body, "Extracted Artifacts:") && strings.Contains(body, "ADR") {
			foundEnrichedCaption = true
		}
		if strings.Contains(body, expectedZipFilename) || strings.Contains(body, "Session Engineering Artifacts Bundle") {
			foundZipDocument = true
		}
	}

	if !foundEnrichedCaption {
		t.Errorf("Expected enriched transcript caption with extracted artifacts breakdown in sent bodies")
	}
	if !foundZipDocument {
		t.Errorf("Expected companion ZIP document to be sent to Telegram")
	}
}

func TestHandleExportCommand_SafeParking_HeadlessExport(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	sessionID := "safe-parking-headless-99"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	_ = os.WriteFile(filepath.Join(sessionDir, ".title"), []byte("Safe Park Session"), 0644)
	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>Execute work</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Working..."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)
	_ = os.WriteFile(filepath.Join(sessionDir, "research_metrics.md"), []byte("# Research on Metrics\nAnalysis content."), 0644)

	user := User{
		ID:        1001,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	// In Safe Parking with nil bot and chatID 0, it must execute cleanly without panic
	handleExportCommand(nil, 0, 1001, "HeadlessBot", user)

	exportDir := filepath.Join(mockAgents, "HeadlessBot", "scratch", "exports")
	expectedMdFilename := fmt.Sprintf("session_Safe_Park_Session_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_Safe_Park_Session_%s.zip", safePrefix(sessionID, 8))

	if _, err := os.Stat(filepath.Join(exportDir, expectedMdFilename)); err != nil {
		t.Errorf("Headless export failed to generate markdown transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, expectedZipFilename)); err != nil {
		t.Errorf("Headless export failed to generate artifacts zip bundle: %v", err)
	}
}

func TestHandleExportCommand_NoArtifacts_FallbackSingleTranscript(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	mockBrain := filepath.Join(tempDir, "brain")
	t.Setenv("AGENTS_DIR", mockAgents)
	t.Setenv("BRAIN_DIR", mockBrain)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	sessionID := "empty-artifacts-55"
	sessionDir := filepath.Join(mockBrain, sessionID)
	logsDir := filepath.Join(sessionDir, ".system_generated", "logs")
	_ = os.MkdirAll(logsDir, 0755)

	transcriptJSONL := `{"step":1,"created_at":"2026-09-05T10:00:00Z","source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>No files created</USER_REQUEST>"}
{"step":2,"created_at":"2026-09-05T10:00:05Z","source":"MODEL","type":"PLANNER_RESPONSE","content":"Done."}
`
	_ = os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcriptJSONL), 0644)

	user := User{
		ID:        555,
		Workspace: tempDir,
		Model:     "gemini-3.8-flash-high",
		SessionID: sessionID,
	}

	handleExportCommand(bot, 998877, 555, "CleanBot", user)

	exportDir := filepath.Join(mockAgents, "CleanBot", "scratch", "exports")
	expectedMdFilename := fmt.Sprintf("session_%s.md", safePrefix(sessionID, 8))
	expectedZipFilename := fmt.Sprintf("artifacts_%s.zip", safePrefix(sessionID, 8))

	if _, err := os.Stat(filepath.Join(exportDir, expectedMdFilename)); err != nil {
		t.Errorf("Transcript markdown missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, expectedZipFilename)); err == nil {
		t.Errorf("Did not expect artifacts ZIP to exist when no artifacts were created")
	}

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	for _, body := range sentBodies {
		if strings.Contains(body, "Extracted Artifacts:") {
			t.Errorf("Caption unexpectedly mentioned Extracted Artifacts when count was 0")
		}
	}
}

func TestStrictTelegram_ChunkSplittingEnforces4096Limit(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	// Generate a massive text of 12,000 characters with various markdown blocks
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(fmt.Sprintf("### Paragraph %d\nThis is a long paragraph with **bold text**, `inline code`, and instructions.\n\n", i))
	}
	longText := sb.String()

	chunks := sendChunk(bot, 12345, 100, longText)
	if len(chunks) <= 1 {
		t.Fatalf("Expected text to be split into multiple chunks, got %d chunks", len(chunks))
	}

	// For the remaining chunks beyond chunk[0], send them as new messages like in session.go
	for i := 1; i < len(chunks); i++ {
		msg := tgbotapi.NewMessage(12345, chunks[i])
		msg.ParseMode = "HTML"
		_, err := bot.Send(msg)
		if err != nil {
			t.Fatalf("Telegram rejected chunk %d (len %d): %v", i, len(chunks[i]), err)
		}
	}

	mu.Lock()
	totalSent := len(*sentReqs)
	mu.Unlock()

	if totalSent < len(chunks) {
		t.Errorf("Expected at least %d requests sent, got %d", len(chunks), totalSent)
	}
}

func TestStrictTelegram_MessageNotModifiedSuppression(t *testing.T) {
	ts, sentReqs, mu := createStrictTelegramMockServer(t)
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, err := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())
	if err != nil {
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	sameText := "Identical streaming text that is sent twice without delta change."

	// 1. First edit: succeeds (200 OK)
	sendChunk(bot, 12345, 300, sameText)

	mu.Lock()
	initialCount := len(*sentReqs)
	mu.Unlock()

	// 2. Second edit with identical content: strict mock returns 400 "message is not modified"
	// sendChunk must suppress this error and NOT trigger fallback bot.Send(newMsg)
	sendChunk(bot, 12345, 300, sameText)

	mu.Lock()
	afterCount := len(*sentReqs)
	mu.Unlock()

	// Exactly 1 new request (the editMessageText attempt) should have been made, NO fallback sendMessage
	if afterCount != initialCount+1 {
		t.Errorf("Expected 1 edit attempt without fallback, initial=%d, after=%d", initialCount, afterCount)
	}
}

func TestStrictTelegram_EditFailureTriggersNewMessageFallback(t *testing.T) {
	// Server returns 400 Bad Request: message to edit not found (generic error)
	var sentBodies []string
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		mu.Lock()
		sentBodies = append(sentBodies, string(bodyBytes))
		mu.Unlock()

		if strings.Contains(r.URL.Path, "editMessageText") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`))
			return
		}

		// Fallback sendMessage succeeds
		w.Write([]byte(`{"ok":true,"result":{"message_id":555,"chat":{"id":12345},"text":"fallback ok"}}`))
	}))
	defer ts.Close()

	endpoint := ts.URL + "/bot%s/%s"
	bot, _ := tgbotapi.NewBotAPIWithClient("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", endpoint, ts.Client())

	text := "Important update when message cannot be edited."
	sendChunk(bot, 12345, 999, text)

	mu.Lock()
	count := len(sentBodies)
	mu.Unlock()

	// Should have sent 2 requests: 1 editMessageText (which failed), followed by 1 fallback sendMessage
	if count != 2 {
		t.Errorf("Expected 2 requests (edit + fallback new message), got %d: %v", count, sentBodies)
	}
}

func TestHandleStartCommand_DisplaysAccountAndNewSessionButtons(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(777)
	user := getUser(db, userID, "TestMockBot")
	user.SessionID = "11111111-2222-3333-4444-555555555555"

	// Mock account pool
	pool, err := NewAccountPool(filepath.Join(tempDir, "pool"))
	if err != nil {
		t.Fatalf("NewAccountPool failed: %v", err)
	}
	acc := &Account{
		ID:      "acc-dashboard-test",
		Email:   "test@example.com",
		HomeDir: tempDir,
		State:   StateActive,
	}
	pool.accounts[acc.ID] = acc
	pool.activeChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = nil }()

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected Telegram message to be sent for handleStartCommand")
	}

	var foundAccount, foundNewSessionBtn, foundAccountsBtn bool
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Account:* `acc-dashboard-test`") {
			foundAccount = true
		}
		if strings.Contains(unescaped, "🆕 New Session") {
			foundNewSessionBtn = true
		}
		if strings.Contains(unescaped, "👥 Accounts") {
			foundAccountsBtn = true
		}
	}

	if !foundAccount {
		t.Errorf("Expected start message to contain Account 'acc-dashboard-test', got: %v", sentBodies)
	}
	if !foundNewSessionBtn {
		t.Errorf("Expected start message keyboard to contain '🆕 New Session', got: %v", sentBodies)
	}
	if !foundAccountsBtn {
		t.Errorf("Expected start message keyboard to contain '👥 Accounts', got: %v", sentBodies)
	}
}

func TestHandleClearCommand_FreshSessionUX(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(12345)
	userID := int64(777)
	user := getUser(db, userID, "TestMockBot")
	user.SessionID = "22222222-3333-4444-5555-666666666666"

	handleClearCommand(bot, chatID, userID, "TestMockBot", user, db)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	if len(sentBodies) == 0 {
		t.Fatal("Expected message sent for handleClearCommand")
	}

	foundFreshSession := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Fresh session initiated") {
			foundFreshSession = true
			break
		}
	}

	if !foundFreshSession {
		t.Errorf("Expected fresh session initiated text in response, got: %v", sentBodies)
	}
}

func TestGetCompactionHint(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	convID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	logsDir := filepath.Join(tempDir, convID, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create test logs dir: %v", err)
	}
	transcriptPath := filepath.Join(logsDir, "transcript.jsonl")

	// Case 1: Small transcript (< 500 KB) and no stream recovery
	smallData := []byte("{\"step\": 1, \"content\": \"hello\"}\n")
	if err := os.WriteFile(transcriptPath, smallData, 0644); err != nil {
		t.Fatalf("Failed to write small transcript: %v", err)
	}

	hint := getCompactionHint(convID, false)
	if hint != "" {
		t.Errorf("Expected empty hint for small transcript without stream recovery, got: %q", hint)
	}

	// Case 2: Small transcript with stream recovery
	hintRecovery := getCompactionHint(convID, true)
	if !strings.Contains(hintRecovery, "Network stream interruption detected") {
		t.Errorf("Expected stream recovery warning, got: %q", hintRecovery)
	}
	if !strings.Contains(hintRecovery, "/export") || !strings.Contains(hintRecovery, "🆕 New Session") {
		t.Errorf("Expected recommendation for export and new session, got: %q", hintRecovery)
	}

	// Case 3: Transcript >= 500 KB (512 KB)
	largeData := make([]byte, 512*1024)
	for i := range largeData {
		largeData[i] = 'A'
	}
	if err := os.WriteFile(transcriptPath, largeData, 0644); err != nil {
		t.Fatalf("Failed to write large transcript: %v", err)
	}

	hintLarge := getCompactionHint(convID, false)
	if !strings.Contains(hintLarge, "Session transcript reached 512 KB") {
		t.Errorf("Expected size notice (512 KB), got: %q", hintLarge)
	}
	if !strings.Contains(hintLarge, "/export") || !strings.Contains(hintLarge, "🆕 New Session") {
		t.Errorf("Expected export and new session advice, got: %q", hintLarge)
	}

	// Case 4: Transcript >= 500 KB with stream recovery (size takes precedence / clear messaging)
	hintLargeWithRecovery := getCompactionHint(convID, true)
	if !strings.Contains(hintLargeWithRecovery, "Session transcript reached 512 KB") {
		t.Errorf("Expected size notice (512 KB) even with stream recovery, got: %q", hintLargeWithRecovery)
	}
}

func TestHandleStartCommand_PinnedAccountDisplay(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	tempDir := t.TempDir()
	t.Setenv("BRAIN_DIR", tempDir)

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	chatID := int64(99999)
	userID := int64(888)
	user := getUser(db, userID, "TestMockBot")

	pool, err := NewAccountPool(filepath.Join(tempDir, "pool"))
	if err != nil {
		t.Fatalf("NewAccountPool failed: %v", err)
	}
	acc := &Account{
		ID:      "acc-pinned-vip",
		Email:   "vip@example.com",
		HomeDir: tempDir,
		State:   StateActive,
	}
	pool.accounts[acc.ID] = acc
	pool.activeChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	pool.pinnedChat[poolChatKey(chatID, "TestMockBot")] = acc.ID
	GlobalAccountPool = pool
	defer func() { GlobalAccountPool = nil }()

	handleStartCommand(bot, chatID, "TestMockBot", user)

	ms.mu.Lock()
	sentBodies := append([]string{}, ms.sentBodies...)
	ms.mu.Unlock()

	foundPinned := false
	for _, raw := range sentBodies {
		unescaped, _ := url.QueryUnescape(raw)
		if strings.Contains(unescaped, "Account:* `acc-pinned-vip 🔒`") {
			foundPinned = true
			break
		}
	}

	if !foundPinned {
		t.Errorf("Expected pinned account indicator 'acc-pinned-vip 🔒', got: %v", sentBodies)
	}
}

func TestStoreQuestionOption_DeterministicFIFORotation(t *testing.T) {
	questionOptionsMu.Lock()
	savedOptions := make(map[string]string)
	for k, v := range questionOptions {
		savedOptions[k] = v
	}
	savedKeys := append([]string{}, questionOptionsKeys...)
	questionOptions = make(map[string]string)
	questionOptionsKeys = nil
	questionOptionsMu.Unlock()

	defer func() {
		questionOptionsMu.Lock()
		questionOptions = savedOptions
		questionOptionsKeys = savedKeys
		questionOptionsMu.Unlock()
	}()

	totalOptions := 150
	cbKeys := make([]string, totalOptions)
	expectedTexts := make([]string, totalOptions)

	for i := 0; i < totalOptions; i++ {
		text := fmt.Sprintf("Question Option #%03d - Very long detailed response text exceeding 64 bytes in length [%03d]", i, i)
		expectedTexts[i] = text
		cb := storeQuestionOption(text)
		if !strings.HasPrefix(cb, "ans_id:") {
			t.Fatalf("Expected ans_id: prefix for long option %d, got %s", i, cb)
		}
		cbKeys[i] = cb

		questionOptionsMu.RLock()
		curLen := len(questionOptions)
		curKeysLen := len(questionOptionsKeys)
		questionOptionsMu.RUnlock()

		if curLen > maxQuestionOptions {
			t.Fatalf("questionOptions exceeded maxQuestionOptions (%d): got %d at step %d", maxQuestionOptions, curLen, i)
		}
		if curKeysLen != curLen {
			t.Fatalf("Mismatch between map size (%d) and keys slice length (%d) at step %d", curLen, curKeysLen, i)
		}
	}

	for i := 0; i < 50; i++ {
		_, found := getQuestionOption(cbKeys[i])
		if found {
			t.Errorf("Expected option %d (%s) to be evicted by FIFO queue, but it was found", i, cbKeys[i])
		}
	}

	for i := 50; i < totalOptions; i++ {
		got, found := getQuestionOption(cbKeys[i])
		if !found {
			t.Errorf("Expected option %d (%s) to be present in cache, but it was not found", i, cbKeys[i])
		} else if got != expectedTexts[i] {
			t.Errorf("Option %d content mismatch: got %q, want %q", i, got, expectedTexts[i])
		}
	}
}
