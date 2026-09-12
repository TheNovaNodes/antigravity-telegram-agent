package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// roundTripFunc allows inline mock implementation of http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAccountPool_FetchEmailForToken_Scenarios(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	// 1. Empty token should fail fast without network call
	email, err := pool.FetchEmailForToken("")
	if err == nil || email != "" {
		t.Fatalf("expected error for empty token, got email: %s, err: %v", email, err)
	}

	// 2. 200 OK with valid email
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer valid-token-123" {
			t.Errorf("expected Bearer valid-token-123, got: %s", req.Header.Get("Authorization"))
		}
		jsonBody := `{"email": "hero@novanodes.com"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("valid-token-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if email != "hero@novanodes.com" {
		t.Errorf("expected hero@novanodes.com, got: %s", email)
	}

	// 3. 200 OK with missing or empty email field
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		jsonBody := `{"email": "   ", "name": "No Email User"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-empty-email")
	if err == nil || email != "" {
		t.Errorf("expected error for missing email, got email: %s, err: %v", email, err)
	}

	// 4. 401 Unauthorized (invalid/expired OAuth token)
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		jsonBody := `{"error": "invalid_token", "error_description": "Token has expired"}`
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-expired")
	if err == nil {
		t.Fatalf("expected error for 401 Unauthorized, got nil")
	}

	// 5. 429 Too Many Requests (Google rate limit)
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(bytes.NewBufferString("Rate limit exceeded")),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-ratelimited")
	if err == nil {
		t.Fatalf("expected error for 429 Too Many Requests, got nil")
	}

	// 6. 500 Internal Server Error
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewBufferString("Google backend crash")),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-google-500")
	if err == nil {
		t.Fatalf("expected error for 500 Internal Server Error, got nil")
	}

	// 7. Malformed JSON
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("{unparseable json")),
			Header:     make(http.Header),
		}, nil
	})

	email, err = pool.FetchEmailForToken("token-malformed-json")
	if err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}

	// 8. Network timeout / connection refused
	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: errors.New("connection reset by peer")}
	})

	email, err = pool.FetchEmailForToken("token-network-drop")
	if err == nil {
		t.Fatalf("expected error for network failure, got nil")
	}
}

func TestAccountPool_DeriveAccountIDFromEmail_Table(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"developer@novanodes.com", "developer"},
		{"john.doe-dev_123@gmail.com", "john.doe-dev_123"},
		{"UPPERCASE.User@COMPANY.COM", "uppercase.user"},
		{"strange!#$special%*()chars@domain.com", "strangespecialchars"},
		{"very_long_prefix_that_exceeds_thirty_two_characters_limit@domain.com", "very_long_prefix_that_exceeds_th"},
		{"", "account"},
		{"@empty-user.com", "account"},
		{"clean-slug", "clean-slug"},
		{"...dots-and-dashes---", "dots-and-dashes"},
	}

	for _, tc := range tests {
		got := DeriveAccountIDFromEmail(tc.input)
		if got != tc.expected {
			t.Errorf("DeriveAccountIDFromEmail(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestAccountPool_IngestCurrentAccount_Scenarios(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	mockHome := filepath.Join(tmpDir, "mock_home")
	_ = os.MkdirAll(mockHome, 0755)
	t.Setenv("HOME", mockHome)

	// Scenario 1: Token file missing
	_, err := pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when token file is missing, got nil")
	}

	// Scenario 2: Token file exists but invalid JSON
	tokenDir := filepath.Join(mockHome, ".gemini", "antigravity-cli")
	_ = os.MkdirAll(tokenDir, 0755)
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")
	_ = os.WriteFile(tokenPath, []byte("broken json payload"), 0600)

	_, err = pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when token file is malformed JSON, got nil")
	}

	// Scenario 3: Token file has empty access token
	_ = os.WriteFile(tokenPath, []byte(`{"token": {"access_token": ""}}`), 0600)
	_, err = pool.IngestCurrentAccount()
	if err == nil {
		t.Fatalf("expected error when access token is empty, got nil")
	}

	// Scenario 4: Successful ingestion
	validTokenJSON := `{"token": {"access_token": "valid-oauth-secret-abc"}}`
	_ = os.WriteFile(tokenPath, []byte(validTokenJSON), 0600)

	pool.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"email": "agent.ingest@novanodes.com"}`)),
			Header:     make(http.Header),
		}, nil
	})

	acc, err := pool.IngestCurrentAccount()
	if err != nil {
		t.Fatalf("unexpected error during ingest: %v", err)
	}
	if acc == nil {
		t.Fatalf("expected ingested account, got nil")
	}
	if acc.ID != "agent.ingest" {
		t.Errorf("expected account ID agent.ingest, got: %s", acc.ID)
	}
	if acc.Email != "agent.ingest@novanodes.com" {
		t.Errorf("expected email agent.ingest@novanodes.com, got: %s", acc.Email)
	}

	// Verify profile folder was created and account added to pool
	pool.mu.RLock()
	retrieved, exists := pool.accounts["agent.ingest"]
	pool.mu.RUnlock()

	if !exists || retrieved == nil {
		t.Errorf("ingested account not found in pool map")
	}
	if _, err := os.Stat(acc.HomeDir); os.IsNotExist(err) {
		t.Errorf("account profile directory was not created at %s", acc.HomeDir)
	}
}

func TestAccountPool_FetchAllQuotas_Execution(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := "#!/bin/sh\necho '{\"command\":{\"data\":{\"groups\":[{\"name\":\"gemini\",\"buckets\":[{\"id\":\"5h\",\"window\":\"5h\",\"remaining_fraction\":0.9,\"reset_time\":\"2026-09-12T15:00:00Z\"}]}]}}}'\n"
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}
	t.Setenv("AGY_BINARY", mockAgy)

	pool.accounts["acc-alpha"] = &Account{
		ID:      "acc-alpha",
		Email:   "alpha@example.com",
		HomeDir: filepath.Join(tmpDir, "acc-alpha"),
		State:   StateActive,
	}
	pool.accounts["acc-beta"] = &Account{
		ID:      "acc-beta",
		Email:   "beta@example.com",
		HomeDir: filepath.Join(tmpDir, "acc-beta"),
		State:   StateCooldown,
	}

	// FetchAllQuotas executes concurrently and waits for all accounts
	pool.FetchAllQuotas()

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if pool.accounts["acc-alpha"].Quota.LastFetchedAt.IsZero() {
		t.Errorf("expected quota to be populated for acc-alpha")
	}
}

func TestAccountState_String(t *testing.T) {
	if StateActive.String() != "Active" {
		t.Errorf("expected Active")
	}
	if StateInUse.String() != "In-Use" {
		t.Errorf("expected In-Use")
	}
	if StateCooldown.String() != "Cooldown" {
		t.Errorf("expected Cooldown")
	}
	if StateExpired.String() != "Expired" {
		t.Errorf("expected Expired")
	}
	if AccountState(99).String() != "Unknown" {
		t.Errorf("expected Unknown")
	}
}

func TestGetAccountsDir_EnvOverride(t *testing.T) {
	t.Setenv("ACCOUNTS_DIR", "/custom/accounts/dir")
	if getAccountsDir() != "/custom/accounts/dir" {
		t.Errorf("expected /custom/accounts/dir, got: %s", getAccountsDir())
	}
}

func TestAccountPool_StartBackgroundReaper_ImmediateCancel(t *testing.T) {
	pool, tmpDir := setupTestAccountPool(t)
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool.StartBackgroundReaper(ctx, nil)
}
