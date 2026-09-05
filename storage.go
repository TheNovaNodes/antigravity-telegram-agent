package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// User represents the User data structure.
type User struct {
	ID           int64
	Workspace    string
	Model        string
	IsFirstStart bool
	SessionID    string
	VoiceReply   bool
}

const defaultModel = "gemini-3.8-flash-high"

// getDataDir resolves the directory for persistent SQLite database files.
func getDataDir() string {
	if env := os.Getenv("DATA_DIR"); env != "" {
		return env
	}
	// Check if data directory exists or create it
	dir := "data"
	if err := os.MkdirAll(dir, 0700); err == nil {
		return dir
	}
	return "."
}

// loadEnvFile secures and loads secrets into the process environment.
// In production mode (ALLOW_DOTENV != "1"), it requires ENV_FILE (defaulting to /etc/antigravity-bot/env).
// If the production env file is missing, it exits via log.Fatalf.
// In development mode (ALLOW_DOTENV == "1"), it falls back to a local .env file.
// All target env files are subject to a fail-closed 0600 permissions check.
func loadEnvFile() {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = "/etc/antigravity-bot/env"
	}

	// #nosec G703 -- gosec:nri (Need Review)
	info, err := os.Stat(envFile)
	if err != nil {
		// Production mode: fail-closed if missing
		if os.Getenv("ALLOW_DOTENV") != "1" {
			log.Fatalf("FATAL [Security]: Production environment file %s not found. Refusing to start in insecure mode. Set ENV_FILE, provision %s, or set ALLOW_DOTENV=1 for local dev.", envFile, envFile)
		}

		// Development mode: fallback to .env in current directory
		envFile = ".env"
		// #nosec G703 -- gosec:nri (Need Review)
		info, err = os.Stat(envFile)
		if err != nil {
			// In dev mode, if neither exists, log warning and rely on already exported environment
			log.Printf("⚠️ Dev mode (ALLOW_DOTENV=1): No env file found at ENV_FILE or .env; continuing with process environment.")
			return
		}
	}

	// Fail-closed permission check: enforce 0600
	if mode := info.Mode().Perm(); mode != 0600 {
		// #nosec G703 -- gosec:nri (Need Review)
		if err := os.Chmod(envFile, 0600); err != nil {
			log.Fatalf("FATAL [Security]: Insecure file permissions on %s (%04o) and failed to enforce 0600: %v", envFile, mode, err)
		}
		log.Printf("🔒 Enforced 0600 permissions on %s (was %04o)", envFile, mode)
	}

	// Parse and populate environment variables
	// #nosec G304 G703 -- gosec:nri (Need Review)
	data, err := os.ReadFile(envFile)
	if err != nil {
		log.Fatalf("FATAL [Security]: Failed to read env file %s: %v", envFile, err)
	}

	lines := strings.Split(string(data), "\n")
	loadedCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if k != "" {
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
			loadedCount++
		}
	}
	log.Printf("🔑 Loaded and verified %d environment variables from %s (mode 0600)", loadedCount, envFile)
}

// ensureEnvPermissions verifies that env permissions are enforced (0600).
func ensureEnvPermissions() {
	loadEnvFile()
}

// initDB initializes the SQLite database for a specific bot, enables WAL mode, and creates necessary tables.
func initDB(botName string) *sql.DB {
	dbDir := getDataDir()
	dbPath := filepath.Join(dbDir, fmt.Sprintf("sessions_%s.db", botName))

	// Ensure secure permissions (0600) on database file to prevent unauthorized local reading
	// #nosec G304 G703 -- gosec:nri (Need Review)
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600); err == nil {
		f.Close()
	}
	// #nosec G703 -- gosec:nri (Need Review)
	if err := os.Chmod(dbPath, 0600); err != nil {
		log.Printf("⚠️ Warning: Failed to enforce 0600 permissions on db %s: %v", dbPath, err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db %s: %v", dbPath, err)
	}

	// SQLite Concurrency Hardening: Single connection to prevent database lock contention
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Performance & Concurrency Hardening: Enable WAL mode and 5s busy timeout (Fail-Fast)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		log.Fatalf("FATAL: Failed to set PRAGMA journal_mode=WAL for %s: %v", botName, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		log.Fatalf("FATAL: Failed to set PRAGMA busy_timeout for %s: %v", botName, err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		log.Fatalf("FATAL: Failed to set PRAGMA synchronous=NORMAL for %s: %v", botName, err)
	}

	_, err = db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		workspace TEXT DEFAULT '',
		model TEXT DEFAULT '%s',
		is_first_start BOOLEAN DEFAULT 1,
		session_id TEXT DEFAULT NULL,
		voice_reply BOOLEAN DEFAULT 0
	)`, defaultModel))
	if err != nil {
		log.Fatal(err)
	}

	// Dynamic migration for existing users table: check if voice_reply column exists
	var hasVoiceReply bool
	rows, err := db.Query("PRAGMA table_info(users)")
	if err == nil {
		for rows.Next() {
			var cid int
			var name, colType string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err == nil {
				if name == "voice_reply" {
					hasVoiceReply = true
					break
				}
			}
		}
		rows.Close()
	} else {
		log.Printf("⚠️ Warning: Failed to inspect table_info(users): %v", err)
	}

	if !hasVoiceReply {
		if _, err := db.Exec("ALTER TABLE users ADD COLUMN voice_reply BOOLEAN DEFAULT 0"); err != nil {
			log.Printf("⚠️ Warning: Failed to add column voice_reply: %v", err)
		}
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS session_history (
		user_id INTEGER,
		session_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, session_id)
	)`)
	if err != nil {
		log.Fatal(err)
	}
	return db
}

// getUser retrieves a user from the database or creates a new entry with default settings if not found.
func getUser(db *sql.DB, userID int64, botName string) User {
	var u User
	var voiceReplyVal int
	if db != nil {
		err := db.QueryRow("SELECT user_id, workspace, model, is_first_start, session_id, COALESCE(voice_reply, 0) FROM users WHERE user_id = ?", userID).Scan(
			&u.ID, &u.Workspace, &u.Model, &u.IsFirstStart, &u.SessionID, &voiceReplyVal)
		if err == nil {
			u.VoiceReply = (voiceReplyVal == 1)
			return u
		}
		if err == sql.ErrNoRows {
			u = User{
				ID:           userID,
				Workspace:    filepath.Join(getAgentsDir(), botName),
				Model:        defaultModel,
				IsFirstStart: true,
				SessionID:    uuid.New().String(),
				VoiceReply:   false,
			}
			_, err = db.Exec("INSERT INTO users (user_id, workspace, model, is_first_start, session_id, voice_reply) VALUES (?, ?, ?, ?, ?, 0)",
				u.ID, u.Workspace, u.Model, u.IsFirstStart, u.SessionID)
			if err != nil {
				log.Printf("DB Error inserting user %d: %v", u.ID, err)
			}
			return u
		}
	}
	if u.ID == 0 {
		u = User{
			ID:           userID,
			Workspace:    filepath.Join(getAgentsDir(), botName),
			Model:        defaultModel,
			IsFirstStart: true,
			SessionID:    uuid.New().String(),
			VoiceReply:   false,
		}
	}
	return u
}

// isSessionOwnedByUser checks whether a given sessionID was initiated by userID.
func isSessionOwnedByUser(db *sql.DB, userID int64, sessionID string) bool {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM session_history WHERE user_id = ? AND session_id = ?", userID, sessionID).Scan(&count)
	if err != nil {
		log.Printf("DB Error checking session ownership for user %d, session %s: %v", userID, sessionID, err)
		return false
	}
	return count > 0
}

// updateUserSession updates the active conversation session ID for a specific user.
func updateUserSession(db *sql.DB, userID int64, sessionID string) {
	if db == nil {
		return
	}
	_, err := db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)
	if err != nil {
		log.Printf("DB Error updating session for user %d: %v", userID, err)
	}
	if sessionID != "" && isValidSessionID(sessionID) {
		_, err = db.Exec("INSERT OR IGNORE INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
		if err != nil {
			log.Printf("DB Error inserting session_history for user %d: %v", userID, err)
		}
	}
}

// updateUserVoiceReply toggles the persistent voice response mode for a user.
func updateUserVoiceReply(db *sql.DB, userID int64, enabled bool) {
	if db == nil {
		return
	}
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.Exec("UPDATE users SET voice_reply = ? WHERE user_id = ?", val, userID)
	if err != nil {
		log.Printf("DB Error updating voice_reply for user %d: %v", userID, err)
	}
}

// updateUserModel updates the selected LLM model for a specific user.
func updateUserModel(db *sql.DB, userID int64, model string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("UPDATE users SET model = ? WHERE user_id = ?", model, userID)
	if err != nil {
		log.Printf("DB Error updating model for user %d: %v", userID, err)
	}
	return err
}
