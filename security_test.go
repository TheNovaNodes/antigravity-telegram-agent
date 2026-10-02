package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStorage_UpdateUserSession_RejectsEmptyOrInvalidSessionID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userID := int64(4001)

	// 1. Initial valid insert
	updateUserSession(db, userID, "valid-session-123")

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("Expected 1 session in history, got %d, err: %v", count, err)
	}

	// 2. Empty session ID should NOT insert into session_history
	updateUserSession(db, userID, "")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Empty session ID should not be inserted, expected count 1, got %d", count)
	}

	// 3. Invalid/Traversal session ID should NOT insert into session_history
	updateUserSession(db, userID, "../../etc/passwd")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Invalid session ID should not be inserted, expected count 1, got %d", count)
	}

	// 4. Another valid session
	updateUserSession(db, userID, "another-valid-session_456")
	err = db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ?", userID).Scan(&count)
	if err != nil || count != 2 {
		t.Errorf("Expected 2 valid sessions, got %d", count)
	}
}

func TestIsSessionOwnedByUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	userA := int64(1001)
	userB := int64(2002)

	updateUserSession(db, userA, "session-owned-by-a")
	updateUserSession(db, userB, "session-owned-by-b")

	// Verify User A owns their session
	if !isSessionOwnedByUser(db, userA, "session-owned-by-a") {
		t.Errorf("User A should own session-owned-by-a")
	}

	// Verify User A does NOT own User B's session
	if isSessionOwnedByUser(db, userA, "session-owned-by-b") {
		t.Errorf("User A must NOT own session-owned-by-b (BOLA vulnerability)")
	}

	// Verify non-existent session
	if isSessionOwnedByUser(db, userA, "nonexistent-session") {
		t.Errorf("Nonexistent session must return false")
	}

	// Verify empty session
	if isSessionOwnedByUser(db, userA, "") {
		t.Errorf("Empty session must return false")
	}

	// Verify nil DB
	if isSessionOwnedByUser(nil, userA, "session-owned-by-a") {
		t.Errorf("Nil DB must return false")
	}
}

func TestBOLA_ResumeCallback_RejectionAndAllowance(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	userVictimID := int64(888)
	userAttackerID := int64(999)

	// Initialize victim and attacker in DB
	victimUser := getUser(db, userVictimID, "TestMockBot")
	attackerUser := getUser(db, userAttackerID, "TestMockBot")

	// Victim created a session
	updateUserSession(db, userVictimID, "victim-private-session-123")

	// Attacker tries to resume victim's session via forged callback query
	cbAttacker := &tgbotapi.CallbackQuery{
		ID:   "cb_attack_1",
		From: &tgbotapi.User{ID: userAttackerID, UserName: "attacker"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 12345},
			MessageID: 101,
		},
		Data: "resume:victim-private-session-123",
	}

	handleCallbackQuery(bot, cbAttacker, attackerUser, "TestMockBot", db)

	// Attacker user session in DB must NOT be changed to victim's session
	checkAttacker := getUser(db, userAttackerID, "TestMockBot")
	if checkAttacker.SessionID == "victim-private-session-123" {
		t.Fatalf("BOLA breach! Attacker was able to resume victim's session: %s", checkAttacker.SessionID)
	}

	// Verify invalid session ID is rejected
	cbInvalid := &tgbotapi.CallbackQuery{
		ID:   "cb_invalid_1",
		From: &tgbotapi.User{ID: userVictimID, UserName: "victim"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 54321},
			MessageID: 102,
		},
		Data: "resume:../../etc/shadow",
	}
	handleCallbackQuery(bot, cbInvalid, victimUser, "TestMockBot", db)

	// Now legitimate user resumes their own session
	cbVictim := &tgbotapi.CallbackQuery{
		ID:   "cb_legit_1",
		From: &tgbotapi.User{ID: userVictimID, UserName: "victim"},
		Message: &tgbotapi.Message{
			Chat:      &tgbotapi.Chat{ID: 54321},
			MessageID: 103,
		},
		Data: "resume:victim-private-session-123",
	}

	handleCallbackQuery(bot, cbVictim, victimUser, "TestMockBot", db)

	// Victim's session should now be active
	checkVictim := getUser(db, userVictimID, "TestMockBot")
	if checkVictim.SessionID == "" {
		t.Errorf("Expected victim session to be active, got empty")
	}
}

func TestDownloadTelegramMedia_ErrorHandlingAndStatus(t *testing.T) {
	tempDir := t.TempDir()
	mockAgents := filepath.Join(tempDir, "agents")
	t.Setenv("AGENTS_DIR", mockAgents)

	// File server returning 404
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer errServer.Close()

	// Mock Telegram API server returning the errServer URL
	ms := newMockServer()
	defer ms.Close()

	ms.customHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"result":{"file_id":"file404","file_path":"%s"}}`, errServer.URL+"/missing.txt")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":99}}`))
	})

	bot := createMockBot(ms)

	_, _, _, err := downloadTelegramMedia(bot, 12345, "file404", ".txt", "test", "", "TestBot")
	if err == nil {
		t.Errorf("Expected error downloading 404 media from server, got nil")
	}
}

func TestIsPathUnderRoot(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		root     string
		expected bool
	}{
		{"Empty path", "", "/home/user/projects", false},
		{"Empty root", "/home/user/projects", "", false},
		{"Exact match", "/home/user/projects", "/home/user/projects", true},
		{"Valid subfile", "/home/user/projects/app/main.go", "/home/user/projects", true},
		{"Valid nested subdir", "/home/user/projects/deep/nested/dir/file.txt", "/home/user/projects", true},
		{"Traversal escaping root", "/home/user/projects/../../etc/passwd", "/home/user/projects", false},
		{"Prefix name collision", "/home/user/projects-fake/file.txt", "/home/user/projects", false},
		{"Relative traversal escaping root", "../../../../../etc/passwd", "/home/user/projects", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isPathUnderRoot(tc.path, tc.root)
			if got != tc.expected {
				t.Errorf("isPathUnderRoot(%q, %q) = %v, expected %v", tc.path, tc.root, got, tc.expected)
			}
		})
	}
}

func TestIsValidSessionID(t *testing.T) {
	tests := []struct {
		name     string
		sid      string
		expected bool
	}{
		{"Valid UUID", "d222263c-33ac-4d27-921a-d4073bc40c07", true},
		{"Valid alphanumeric", "session_123_abc-456", true},
		{"Empty string", "", false},
		{"Whitespace only", "   ", false},
		{"Path traversal slash", "session/123", false},
		{"Path traversal dot-dot", "../etc/passwd", false},
		{"Command injection chars", "session;rm -rf /", false},
		{"Newline injection", "session\nid", false},
		{"Null byte", "session\x00id", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidSessionID(tc.sid)
			if got != tc.expected {
				t.Errorf("isValidSessionID(%q) = %v, expected %v", tc.sid, got, tc.expected)
			}
		})
	}
}

func TestLoadEnvFile_Permissions(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current wd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	t.Setenv("ALLOW_DOTENV", "1")
	t.Setenv("ENV_FILE", filepath.Join(tempDir, "nonexistent_prod_env"))

	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte("TEST=1\n"), 0644); err != nil {
		t.Fatalf("Failed to create test .env: %v", err)
	}

	loadEnvFile()

	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatalf("Failed to stat .env: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("Expected .env permissions to be 0600, got %o", mode)
	}
}

func TestHandleWorkspaceCommand_Security(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ws_sec_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	projectsDir := filepath.Join(tempDir, "projects")
	agentsDir := filepath.Join(tempDir, "agents")
	os.MkdirAll(projectsDir, 0755)
	os.MkdirAll(filepath.Join(agentsDir, "TestBot"), 0755)
	os.MkdirAll(filepath.Join(agentsDir, "OtherBot"), 0755)

	t.Setenv("PROJECTS_DIR", projectsDir)
	t.Setenv("AGENTS_DIR", agentsDir)

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE users (user_id INTEGER PRIMARY KEY, workspace TEXT, model TEXT, first_start INTEGER, session_id TEXT, voice_reply INTEGER);`); err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, workspace, model, first_start, session_id, voice_reply) VALUES (3001, '/initial', 'gemini-3.7-flash-high', 0, 'session_1', 0);`); err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	bot := &tgbotapi.BotAPI{}
	botName := "TestBot"
	user := User{ID: 3001, Workspace: "/initial", Model: "gemini-3.7-flash-high"}

	sessionKey := fmt.Sprintf("%s:%d:%d", botName, int64(12345), user.ID)
	defer func() {
		sessionMu.Lock()
		if s, ok := globalSessions[sessionKey]; ok {
			s.Kill()
			delete(globalSessions, sessionKey)
		}
		sessionMu.Unlock()
	}()

	// 1. Valid workspace in PROJECTS_DIR
	validProj := filepath.Join(projectsDir, "my_app")
	os.MkdirAll(validProj, 0755)
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+validProj, botName, user, db)

	var ws1 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws1)
	if ws1 != validProj {
		t.Errorf("Expected workspace in DB to be updated to %s, got %s", validProj, ws1)
	}

	// 2. Forbidden workspace in OtherBot's office
	forbiddenOffice := filepath.Join(agentsDir, "OtherBot")
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+forbiddenOffice, botName, user, db)

	var ws2 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws2)
	if ws2 != validProj {
		t.Errorf("Expected workspace update to OtherBot office to be blocked, but DB was changed to %s", ws2)
	}

	// 3. Forbidden workspace in /etc
	handleWorkspaceCommand(bot, 12345, 3001, "/workspace /etc", botName, user, db)
	var ws3 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws3)
	if ws3 != validProj {
		t.Errorf("Expected workspace update to /etc to be blocked, but DB was changed to %s", ws3)
	}

	// 4. Valid workspace in SYSTEM_HOME projects and sysBotOffice
	sysHome := filepath.Join(tempDir, "sysroot")
	sysProj := filepath.Join(sysHome, "projects", "sys_app")
	sysOffice := filepath.Join(sysHome, ".agents", botName)
	os.MkdirAll(sysProj, 0755)
	os.MkdirAll(sysOffice, 0755)
	t.Setenv("SYSTEM_HOME", sysHome)

	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+sysProj, botName, user, db)
	var ws4 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws4)
	if ws4 != sysProj {
		t.Errorf("Expected workspace update to sysProjectsDir %s, got %s", sysProj, ws4)
	}

	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+sysOffice, botName, user, db)
	var ws5 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws5)
	if ws5 != sysOffice {
		t.Errorf("Expected workspace update to sysBotOffice %s, got %s", sysOffice, ws5)
	}

	// 5. Block /tmp/projects escape when baseHome falls back to os.TempDir()
	t.Setenv("SYSTEM_HOME", "")
	t.Setenv("HOME", filepath.Join(tempDir, "etc/antigravity-bot/accounts/user"))
	tmpProjects := filepath.Join(os.TempDir(), "projects", "fake_proj")
	os.MkdirAll(tmpProjects, 0755)
	defer os.RemoveAll(tmpProjects)

	handleWorkspaceCommand(bot, 12345, 3001, "/workspace "+tmpProjects, botName, user, db)
	var ws6 string
	db.QueryRow("SELECT workspace FROM users WHERE user_id = 3001").Scan(&ws6)
	if ws6 == tmpProjects {
		t.Errorf("Security boundary failed: /workspace allowed escape to /tmp/projects %s", tmpProjects)
	}
}

// TestLoadEnvFile_ProductionSuccess validates loading environment variables from ENV_FILE
// and enforcing 0600 permissions.
func TestLoadEnvFile_ProductionSuccess(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, "prod_env")
	envContent := `# Comment line
BOT_TOKENS="12345:tokenA,67890:tokenB"
ALLOWED_ADMIN_IDS='1001,1002'
export CUSTOM_SETTING=production_value
EMPTY_LINE_BELOW

`
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		t.Fatalf("Failed to write test env: %v", err)
	}

	t.Setenv("ENV_FILE", envPath)
	t.Setenv("ALLOW_DOTENV", "")
	t.Setenv("BOT_TOKENS", "")
	t.Setenv("ALLOWED_ADMIN_IDS", "")
	t.Setenv("CUSTOM_SETTING", "")

	loadEnvFile()

	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("Failed to stat env file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("Expected permissions 0600, got %o", mode)
	}

	if os.Getenv("BOT_TOKENS") != "12345:tokenA,67890:tokenB" {
		t.Errorf("Expected BOT_TOKENS to be loaded, got %q", os.Getenv("BOT_TOKENS"))
	}
	if os.Getenv("ALLOWED_ADMIN_IDS") != "1001,1002" {
		t.Errorf("Expected ALLOWED_ADMIN_IDS to be loaded, got %q", os.Getenv("ALLOWED_ADMIN_IDS"))
	}
	if os.Getenv("CUSTOM_SETTING") != "production_value" {
		t.Errorf("Expected CUSTOM_SETTING to be loaded, got %q", os.Getenv("CUSTOM_SETTING"))
	}
}

// TestLoadEnvFile_DevFallbackDotEnv validates falling back to .env when ALLOW_DOTENV=1.
func TestLoadEnvFile_DevFallbackDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get wd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte("DEV_KEY=dev_val\n"), 0644); err != nil {
		t.Fatalf("Failed to write .env: %v", err)
	}

	t.Setenv("ENV_FILE", filepath.Join(tempDir, "nonexistent_prod_env"))
	t.Setenv("ALLOW_DOTENV", "1")
	t.Setenv("DEV_KEY", "")

	loadEnvFile()

	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatalf("Failed to stat .env: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("Expected .env permissions 0600, got %o", mode)
	}
	if os.Getenv("DEV_KEY") != "dev_val" {
		t.Errorf("Expected DEV_KEY=dev_val, got %q", os.Getenv("DEV_KEY"))
	}
}

// TestLoadEnvFile_DevMissingFilesGraceful validates that ALLOW_DOTENV=1 continues gracefully
// if no env file exists.
func TestLoadEnvFile_DevMissingFilesGraceful(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get wd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	t.Setenv("ENV_FILE", filepath.Join(tempDir, "nonexistent_env"))
	t.Setenv("ALLOW_DOTENV", "1")

	// Should not panic or fatal
	loadEnvFile()
}

// TestLoadEnvFile_ProductionMissingFailsClosed validates that missing ENV_FILE in production exits fatally.
func TestLoadEnvFile_ProductionMissingFailsClosed(t *testing.T) {
	if os.Getenv("BE_CRASHING_ENV_TEST") == "1" {
		loadEnvFile()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLoadEnvFile_ProductionMissingFailsClosed")
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(),
		"BE_CRASHING_ENV_TEST=1",
		"ENV_FILE=/tmp/nonexistent_env_file_for_test",
		"ALLOW_DOTENV=",
	)
	err := cmd.Run()
	if e, ok := err.(*exec.ExitError); ok && !e.Success() {
		return // Succeeded in crashing fail-closed
	}
	t.Fatalf("Expected loadEnvFile to exit with non-zero in production when ENV_FILE is missing, got: %v", err)
}

// TestBuildChildEnv_SentinelSecretsEliminated verifies that sensitive supervisor secrets
// (BOT_TOKENS, ALL_TOKENS, ALLOWED_ADMIN_IDS, API keys, passwords, database URLs) are NEVER
// inherited by child CLI environments (#282).
func TestBuildChildEnv_SentinelSecretsEliminated(t *testing.T) {
	sentinels := map[string]string{
		"BOT_TOKENS":                 "12345:tokenA,67890:tokenB",
		"ALL_TOKENS":                 "secret_telegram_token",
		"ALLOWED_ADMIN_IDS":          "999999,888888",
		"ELEVENLABS_API_KEY":         "el_live_secret_key_abcdef123456",
		"SENTINEL_SUPERVISOR_SECRET": "super_sensitive_password_xyz",
		"DATABASE_URL":               "postgres://admin:password@localhost/db",
		"NEXTCLOUD_PASSWORD":         "nextcloud_top_secret",
		"GITHUB_TOKEN":               "ghp_sentinel_leaked_token_12345",
	}

	for k, v := range sentinels {
		t.Setenv(k, v)
	}

	tempHome := t.TempDir()
	customTmp := filepath.Join(tempHome, "tmp")
	_ = os.MkdirAll(customTmp, 0700)

	// Also ensure safe passthrough variables can pass through cleanly
	t.Setenv("USER", "sentinel-bot-runner")
	t.Setenv("SYSTEM_HOME", "/custom/system/home")
	t.Setenv("TMPDIR", customTmp)

	childEnv := buildChildEnv(tempHome, map[string]string{"EXTRA_TEST_FLAG": "enabled"})

	// Convert slice to map for fast lookup
	envMap := make(map[string]string)
	for _, entry := range childEnv {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 1. Assert ZERO sensitive sentinels leaked
	for k := range sentinels {
		if val, exists := envMap[k]; exists {
			t.Errorf("CRITICAL LEAK: Sensitive variable %s leaked into child environment: %q", k, val)
		}
	}

	// 2. Assert safe baseline variables are present
	if envMap["HOME"] != tempHome {
		t.Errorf("Expected HOME=%s, got %s", tempHome, envMap["HOME"])
	}
	if envMap["LANG"] != "C.UTF-8" {
		t.Errorf("Expected LANG=C.UTF-8, got %s", envMap["LANG"])
	}
	if envMap["LC_ALL"] != "C.UTF-8" {
		t.Errorf("Expected LC_ALL=C.UTF-8, got %s", envMap["LC_ALL"])
	}
	if envMap["TZ"] != "UTC" {
		t.Errorf("Expected TZ=UTC, got %s", envMap["TZ"])
	}
	if envMap["TERM"] != "xterm-256color" {
		t.Errorf("Expected TERM=xterm-256color, got %s", envMap["TERM"])
	}
	if envMap["USER"] != "sentinel-bot-runner" {
		t.Errorf("Expected USER=sentinel-bot-runner, got %s", envMap["USER"])
	}
	if envMap["SYSTEM_HOME"] != "/custom/system/home" {
		t.Errorf("Expected SYSTEM_HOME=/custom/system/home, got %s", envMap["SYSTEM_HOME"])
	}
	if envMap["TMPDIR"] != customTmp {
		t.Errorf("Expected TMPDIR=%s, got %s", customTmp, envMap["TMPDIR"])
	}
	if envMap["EXTRA_TEST_FLAG"] != "enabled" {
		t.Errorf("Expected EXTRA_TEST_FLAG=enabled, got %s", envMap["EXTRA_TEST_FLAG"])
	}
	if envMap["PATH"] == "" {
		t.Errorf("Expected PATH to be non-empty")
	}
	if envMap["GOPATH"] == "" || envMap["GOCACHE"] == "" || envMap["NPM_CONFIG_CACHE"] == "" || envMap["PIP_CACHE_DIR"] == "" {
		t.Errorf("Expected build caches (GOPATH, GOCACHE, NPM, PIP) to be set, got %+v", envMap)
	}
}

// TestBuildChildEnv_EmptyAccountHomeFallback verifies that when accountHome is empty,
// buildChildEnv still constructs a safe allowlisted environment (never nil), sets a safe fallback HOME,
// includes SHELL if present, and never leaks supervisor secrets (#282).
func TestBuildChildEnv_EmptyAccountHomeFallback(t *testing.T) {
	t.Setenv("SENTINEL_LEAK_CHECK", "top_secret_value")
	t.Setenv("SYSTEM_HOME", "/custom/sys/home")
	t.Setenv("SHELL", "/bin/bash")

	childEnv := buildChildEnv("")
	if childEnv == nil {
		t.Fatal("buildChildEnv(\"\") returned nil, which would cause exec.Cmd to inherit os.Environ()!")
	}

	envMap := make(map[string]string)
	for _, entry := range childEnv {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if _, exists := envMap["SENTINEL_LEAK_CHECK"]; exists {
		t.Errorf("Critical leak: SENTINEL_LEAK_CHECK was inherited in child environment!")
	}

	if envMap["HOME"] != "/custom/sys/home" {
		t.Errorf("Expected fallback HOME=/custom/sys/home, got %s", envMap["HOME"])
	}

	if envMap["SHELL"] != "/bin/bash" {
		t.Errorf("Expected SHELL=/bin/bash, got %s", envMap["SHELL"])
	}
}

func TestSystemdSandboxingConfigInDeployScript(t *testing.T) {
	deployScriptPath := filepath.Join("scripts", "deploy.sh")
	data, err := os.ReadFile(deployScriptPath)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", deployScriptPath, err)
	}

	content := string(data)

	requiredDirectives := []string{
		"StartLimitIntervalSec=60s",
		"StartLimitBurst=5",
		"Restart=always",
		"RestartSec=3",
		"EnvironmentFile=${PROD_ENV_FILE}",
		`ENV_DIR="/etc/antigravity-bot"`,
	}

	for _, dir := range requiredDirectives {
		if !strings.Contains(content, dir) {
			t.Errorf("deploy.sh missing expected systemd directive: %q", dir)
		}
	}

	// Ensure restrictive sandbox directives that hamper devops/coding agents are NOT present
	forbiddenDirectives := []string{
		"ProtectSystem=",
		"InaccessiblePaths=",
		"ProtectKernelTunables=",
		"ProtectKernelModules=",
		"ProtectControlGroups=",
		"NoNewPrivileges=",
		"PrivateTmp=",
	}

	for _, dir := range forbiddenDirectives {
		if strings.Contains(content, dir) {
			t.Errorf("deploy.sh contains restrictive sandbox directive that hampers coding agents: %q", dir)
		}
	}

	// Verify syntax using systemd-analyze if installed
	if systemdAnalyzePath, err := exec.LookPath("systemd-analyze"); err == nil {
		tempDir := t.TempDir()
		unitFile := filepath.Join(tempDir, "test-antigravity.service")

		// Minimal mock unit matching the deploy script template
		mockUnit := `[Unit]
Description=Test Antigravity Service
After=network.target
StartLimitIntervalSec=60s
StartLimitBurst=5

[Service]
Type=simple
ExecStart=/bin/true
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`
		if err := os.WriteFile(unitFile, []byte(mockUnit), 0644); err != nil {
			t.Fatalf("Failed to write mock unit file: %v", err)
		}

		cmd := exec.Command(systemdAnalyzePath, "verify", unitFile)
		cmd.WaitDelay = 2 * time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("systemd-analyze verify failed: %v, output: %s", err, string(output))
		}
	}
}

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
