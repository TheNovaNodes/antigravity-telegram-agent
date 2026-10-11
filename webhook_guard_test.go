package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestCalculateJitterBackoff_ExponentialGrowthAndCap verifies that exponential backoff
// with full jitter respects the AWS formula, stays bounded within [0, min(cap, base*2^n)),
// and never overflows standard integer types even under extreme failure counters (#370).
func TestCalculateJitterBackoff_ExponentialGrowthAndCap(t *testing.T) {
	base := 1 * time.Second
	maxCap := 30 * time.Second

	tests := []struct {
		name     string
		failures int
		maxBound time.Duration
	}{
		{name: "Negative failures fallback to 0", failures: -5, maxBound: 1 * time.Second},
		{name: "0 failures (attempt 1)", failures: 0, maxBound: 1 * time.Second},
		{name: "1 failure (attempt 2)", failures: 1, maxBound: 2 * time.Second},
		{name: "2 failures (attempt 3)", failures: 2, maxBound: 4 * time.Second},
		{name: "3 failures (attempt 4)", failures: 3, maxBound: 8 * time.Second},
		{name: "4 failures (attempt 5)", failures: 4, maxBound: 16 * time.Second},
		{name: "5 failures (attempt 6)", failures: 5, maxBound: 30 * time.Second}, // 32s capped at 30s
		{name: "10 failures (max bit shift guard)", failures: 10, maxBound: 30 * time.Second},
		{name: "100 failures (overflow protection)", failures: 100, maxBound: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				d := calculateJitterBackoff(tt.failures, base, maxCap)
				if d < 0 {
					t.Fatalf("backoff duration must never be negative, got: %v", d)
				}
				if d >= tt.maxBound {
					t.Fatalf("backoff duration %v exceeded maxBound %v", d, tt.maxBound)
				}
			}
		})
	}
}

// TestCalculateJitterBackoff_Entropy verifies that the full jitter calculation produces
// distinct pseudo-random values across iterations, guaranteeing that bots will not synchronize (#370).
func TestCalculateJitterBackoff_Entropy(t *testing.T) {
	base := 1 * time.Second
	maxCap := 30 * time.Second
	failures := 4 // bound is 16s

	uniqueValues := make(map[time.Duration]struct{})
	samples := 50

	for i := 0; i < samples; i++ {
		d := calculateJitterBackoff(failures, base, maxCap)
		uniqueValues[d] = struct{}{}
	}

	// In 50 samples from a 16-second range, we expect virtually all values to be distinct.
	if len(uniqueValues) < 40 {
		t.Errorf("insufficient entropy in jitter calculation: expected >= 40 unique values from %d samples, got %d", samples, len(uniqueValues))
	}
}

// TestCalculateJitterBackoff_CustomParameters verifies custom base and cap durations.
func TestCalculateJitterBackoff_CustomParameters(t *testing.T) {
	base := 50 * time.Millisecond
	maxCap := 200 * time.Millisecond

	// Even with 10 failures, duration must be strictly < maxCap
	for i := 0; i < 30; i++ {
		d := calculateJitterBackoff(10, base, maxCap)
		if d < 0 || d >= maxCap {
			t.Fatalf("expected 0 <= d < %v, got %v", maxCap, d)
		}
	}

	// With invalid zero parameters, default constants are applied
	dDefault := calculateJitterBackoff(0, 0, 0)
	if dDefault < 0 || dDefault >= defaultGetUpdatesBackoffBase {
		t.Fatalf("expected fallback to default base %v, got %v", defaultGetUpdatesBackoffBase, dDefault)
	}
}

// TestGetUpdatesRecovery_JitterBackoff tests that getUpdatesWithRecovery handles
// transient network errors gracefully and delivers updates upon recovery (#370).
func TestGetUpdatesRecovery_JitterBackoff(t *testing.T) {
	var mu sync.Mutex
	reqCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":777,"is_bot":true,"username":"JitterTestBot"}}`))
			return
		}

		if strings.Contains(r.URL.Path, "getUpdates") {
			mu.Lock()
			reqCount++
			n := reqCount
			mu.Unlock()

			if n == 1 {
				// 1st request: simulate network deadline exceeded / upstream 502
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(`{"ok":false,"error_code":502,"description":"Bad Gateway: upstream telegram timeout"}`))
				return
			}

			// 2nd request: successful update recovery
			resp := map[string]interface{}{
				"ok": true,
				"result": []map[string]interface{}{
					{
						"update_id": 9001,
						"message": map[string]interface{}{
							"message_id": 10,
							"chat":       map[string]interface{}{"id": 12345},
							"from":       map[string]interface{}{"id": 12345},
							"text":       "Jitter backoff test update",
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
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

	updatesChan := getUpdatesWithRecovery(bot, u, nil, stopChan)

	select {
	case update, ok := <-updatesChan:
		if !ok {
			t.Fatal("updates channel closed prematurely")
		}
		if update.UpdateID != 9001 {
			t.Errorf("expected update ID 9001, got %d", update.UpdateID)
		}
		if update.Message == nil || update.Message.Text != "Jitter backoff test update" {
			t.Errorf("unexpected message payload: %v", update.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for update after jitter backoff recovery")
	}

	mu.Lock()
	finalCount := reqCount
	mu.Unlock()

	if finalCount < 2 {
		t.Errorf("expected at least 2 getUpdates requests (1 fail + 1 success), got %d", finalCount)
	}
}

// TestGetUpdatesRecovery_InterruptionDuringBackoff verifies that closing stopChan
// interrupts the backoff sleep immediately without leaking goroutines (#370).
func TestGetUpdatesRecovery_InterruptionDuringBackoff(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/getMe") {
			w.Write([]byte(`{"ok":true,"result":{"id":666,"is_bot":true,"username":"InterruptBot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getUpdates") {
			// Always return server error to force backoff sleep
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal error"}`))
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

	// Wait briefly for first error to trigger backoff
	time.Sleep(50 * time.Millisecond)

	// Close stopChan during backoff sleep
	close(stopChan)

	select {
	case _, ok := <-updatesChan:
		if ok {
			for range updatesChan {
			}
		}
	case <-time.After(1 * time.Second):
		t.Fatal("getUpdatesWithRecovery failed to terminate promptly when stopChan closed during backoff")
	}
}
