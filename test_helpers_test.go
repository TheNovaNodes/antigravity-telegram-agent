package main

import (
	"bytes"
	"database/sql"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setupTestAccountPool(t *testing.T) (*AccountPool, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "account_pool_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	pool, err := NewAccountPool(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create account pool: %v", err)
	}
	return pool, tmpDir
}

func containsAll(str string, substrs ...string) bool {
	for _, s := range substrs {
		if !strings.Contains(str, s) {
			return false
		}
	}
	return true
}

// roundTripFunc allows inline mock implementation of http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func setupTestDBAndBot(t *testing.T) (*sql.DB, *tgbotapi.BotAPI, *httptestServerHelper) {
	t.Helper()
	ts, sent, mu := createStrictTelegramMockServer(t)

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("MOCK_TOKEN", ts.URL+"/bot%s/%s")
	if err != nil {
		ts.Close()
		t.Fatalf("Failed to create mock bot: %v", err)
	}

	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)
	db := initDB("TestInteractiveBot")

	helper := &httptestServerHelper{
		server: ts,
		sent:   sent,
		mu:     mu,
	}
	return db, bot, helper
}

type httptestServerHelper struct {
	server interface{ Close() }
	sent   *[]string
	mu     interface {
		Lock()
		Unlock()
	}
}

func (h *httptestServerHelper) Close() {
	h.server.Close()
}

func (h *httptestServerHelper) getLastSentText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(*h.sent) == 0 {
		return ""
	}
	last := (*h.sent)[len(*h.sent)-1]
	vals, _ := url.ParseQuery(last)
	return vals.Get("text")
}

func setupTestDB(t *testing.T) *sql.DB {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '',
		model TEXT DEFAULT 'gemini-3.1-pro-high',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL,
		voice_reply BOOLEAN DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS session_history (
		user_id INTEGER,
		session_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		is_orphaned BOOLEAN DEFAULT 0,
		UNIQUE(user_id, session_id)
	)`)
	if err != nil {
		t.Fatalf("Failed to create session_history table: %v", err)
	}
	return db
}

func getCounterValue(counter prometheus.Counter) float64 {
	var m dto.Metric
	if err := counter.Write(&m); err != nil {
		return 0
	}
	if m.Counter == nil {
		return 0
	}
	return m.Counter.GetValue()
}

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

// createStrictTelegramMockServer creates an HTTP server that simulates real Telegram API edge cases:
// 1. Strict length limit: rejects text > 4096 chars with 400 Bad Request: message is too long.
// 2. Strict edit check: rejects editMessageText with identical text with 400 Bad Request: message is not modified.
func createStrictTelegramMockServer(t *testing.T) (*httptest.Server, *[]string, *sync.Mutex) {
	var mu sync.Mutex
	var sentRequests []string
	lastEditedText := make(map[int]string)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		mu.Lock()
		sentRequests = append(sentRequests, string(bodyBytes))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"StrictBot","username":"StrictBot"}}`))
			return
		}

		vals, _ := url.ParseQuery(string(bodyBytes))
		text := vals.Get("text")

		// Rule 1: Real Telegram 4096 character hard limit
		if len([]rune(text)) > 4096 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`))
			return
		}

		// Rule 2: Real Telegram message is not modified
		if strings.Contains(r.URL.Path, "editMessageText") {
			msgIDStr := vals.Get("message_id")
			var msgID int
			fmt.Sscanf(msgIDStr, "%d", &msgID)

			mu.Lock()
			prev, seen := lastEditedText[msgID]
			if seen && prev == text {
				mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}`))
				return
			}
			lastEditedText[msgID] = text
			mu.Unlock()
		}

		w.Write([]byte(`{"ok":true,"result":{"message_id":200,"chat":{"id":12345},"text":"ok"}}`))
	}))

	return ts, &sentRequests, &mu
}

func getSentTelegramMessages(ms *mockServer) []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var msgs []string
	for i, req := range ms.sentRequests {
		if strings.Contains(req.URL.Path, "sendMessage") {
			msgs = append(msgs, ms.sentBodies[i])
		}
	}
	return msgs
}

func getSentTelegramMessagesOrEdits(ms *mockServer) []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var msgs []string
	for i, req := range ms.sentRequests {
		if strings.Contains(req.URL.Path, "sendMessage") || strings.Contains(req.URL.Path, "editMessageText") {
			msgs = append(msgs, ms.sentBodies[i])
		}
	}
	return msgs
}
