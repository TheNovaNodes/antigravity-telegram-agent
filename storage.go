package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
	if err := os.MkdirAll(dir, 0755); err == nil {
		return dir
	}
	return "."
}

// initDB initializes the SQLite database for a specific bot, enables WAL mode, and creates necessary tables.
func initDB(botName string) *sql.DB {
	dbDir := getDataDir()
	dbPath := filepath.Join(dbDir, fmt.Sprintf("sessions_%s.db", botName))

	// Ensure secure permissions (0600) on database file to prevent unauthorized local reading
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600); err == nil {
		f.Close()
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db %s: %v", dbPath, err)
	}

	// SQLite Concurrency Hardening: Single connection to prevent database lock contention
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Performance & Concurrency Hardening: Enable WAL mode and 5s busy timeout
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		log.Printf("⚠️ Warning: Failed to set PRAGMA journal_mode=WAL for %s: %v", botName, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		log.Printf("⚠️ Warning: Failed to set PRAGMA busy_timeout for %s: %v", botName, err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL;"); err != nil {
		log.Printf("⚠️ Warning: Failed to set PRAGMA synchronous=NORMAL for %s: %v", botName, err)
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

// updateUserSession updates the active conversation session ID for a specific user.
func updateUserSession(db *sql.DB, userID int64, sessionID string) {
	if db == nil {
		return
	}
	_, err := db.Exec("UPDATE users SET session_id = ? WHERE user_id = ?", sessionID, userID)
	if err != nil {
		log.Printf("DB Error updating session for user %d: %v", userID, err)
	}
	_, err = db.Exec("INSERT OR IGNORE INTO session_history (user_id, session_id) VALUES (?, ?)", userID, sessionID)
	if err != nil {
		log.Printf("DB Error inserting session_history for user %d: %v", userID, err)
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
