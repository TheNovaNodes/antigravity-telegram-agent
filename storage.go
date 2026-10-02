package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
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

var migrateOnce sync.Once

// getDataDir resolves the directory for persistent SQLite database files.
// Priority:
// 1. DATA_DIR environment variable (explicit override)
// 2. /var/lib/antigravity-bot/data (FHS standard for system daemon persistent state)
// 3. /etc/antigravity-bot/data (FHS fallback for configuration/state)
// 4. ./data (local development / testing fallback)
// 5. . (current working directory fallback)
func getDataDir() string {
	if env := os.Getenv("DATA_DIR"); env != "" {
		cleaned := filepath.Clean(env)
		_ = os.MkdirAll(cleaned, 0700)
		return cleaned
	}

	candidates := []string{
		"/var/lib/antigravity-bot/data",
		"/etc/antigravity-bot/data",
	}

	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0700); err == nil {
			return dir
		}
	}

	dir := "data"
	if err := os.MkdirAll(dir, 0700); err == nil {
		return dir
	}
	return "."
}

// migrateLegacyDataFiles checks if legacy databases exist in fallback "./data" directory
// and safely copies them to targetDir if they are not already present in targetDir.
// It skips automatic migration during testing unless AUTO_MIGRATE_DATA=1.
func migrateLegacyDataFiles(targetDir string) {
	if isTestEnvironment() && os.Getenv("AUTO_MIGRATE_DATA") != "1" {
		return
	}
	legacyDir := "data"
	count, err := MigrateDataFiles(legacyDir, targetDir)
	if err != nil {
		log.Printf("⚠️ [Storage Migration] Partial migration notice from %s to %s: %v", legacyDir, targetDir, err)
	} else if count > 0 {
		log.Printf("📦 [Storage Migration] Successfully migrated %d database file(s) from %s to %s (mode 0600)", count, legacyDir, targetDir)
	}
}

// isTestEnvironment detects if the process is running within a go test runner.
func isTestEnvironment() bool {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.") {
			return true
		}
	}
	return strings.HasSuffix(os.Args[0], ".test")
}

// MigrateDataFiles safely copies SQLite database files (.db, .db-wal, .db-shm)
// from srcDir to dstDir preserving 0600 file permissions and avoiding overwriting existing files in dstDir.
// Returns count of migrated files.
func MigrateDataFiles(srcDir, dstDir string) (int, error) {
	cleanSrc, errSrc := filepath.Abs(srcDir)
	cleanDst, errDst := filepath.Abs(dstDir)
	if errSrc == nil && errDst == nil && cleanSrc == cleanDst {
		return 0, nil
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return 0, err
	}

	migratedCount := 0
	var lastErr error

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Match SQLite database files and journal/WAL files
		if !strings.HasPrefix(name, "sessions_") || (!strings.HasSuffix(name, ".db") && !strings.HasSuffix(name, ".db-wal") && !strings.HasSuffix(name, ".db-shm")) {
			continue
		}

		srcPath := filepath.Join(srcDir, name)
		dstPath := filepath.Join(dstDir, name)

		// Check if destination file already exists
		if _, err := os.Stat(dstPath); err == nil {
			// Already exists, do not overwrite to protect existing target state
			continue
		}

		// Copy file securely with 0600 mode
		if err := copyFileSecure(srcPath, dstPath, 0600); err != nil {
			lastErr = err
			log.Printf("⚠️ Warning: Failed to copy %s to %s: %v", srcPath, dstPath, err)
		} else {
			migratedCount++
		}
	}

	return migratedCount, lastErr
}

// copyFileSecure copies data from src to dst and enforces the specified file mode.
func copyFileSecure(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
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

	info, err := os.Stat(envFile)
	if err != nil {
		// Production mode: fail-closed if missing
		if os.Getenv("ALLOW_DOTENV") != "1" {
			log.Fatalf("FATAL [Security]: Production environment file %s not found. Refusing to start in insecure mode. Set ENV_FILE, provision %s, or set ALLOW_DOTENV=1 for local dev.", envFile, envFile)
		}

		// Development mode: fallback to .env in current directory
		envFile = ".env"
		info, err = os.Stat(envFile)
		if err != nil {
			// In dev mode, if neither exists, log warning and rely on already exported environment
			log.Printf("⚠️ Dev mode (ALLOW_DOTENV=1): No env file found at ENV_FILE or .env; continuing with process environment.")
			return
		}
	}

	// Fail-closed permission check: enforce 0600
	if mode := info.Mode().Perm(); mode != 0600 {
		if err := os.Chmod(envFile, 0600); err != nil {
			log.Fatalf("FATAL [Security]: Insecure file permissions on %s (%04o) and failed to enforce 0600: %v", envFile, mode, err)
		}
		log.Printf("🔒 Enforced 0600 permissions on %s (was %04o)", envFile, mode)
	}

	// Parse and populate environment variables
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

// initDB initializes the SQLite database for a specific bot, enables WAL mode, and creates necessary tables.
func initDB(botName string) *sql.DB {
	dbDir := getDataDir()
	migrateOnce.Do(func() {
		migrateLegacyDataFiles(dbDir)
	})
	dbPath := filepath.Join(dbDir, fmt.Sprintf("sessions_%s.db", botName))

	db, err := sql.Open("sqlite", dbPath)
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

	// Ensure secure permissions (0600) on database file to prevent unauthorized local reading
	if err := os.Chmod(dbPath, 0600); err != nil {
		log.Printf("⚠️ Warning: Failed to enforce 0600 permissions on db %s: %v", dbPath, err)
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
		is_orphaned BOOLEAN DEFAULT 0,
		UNIQUE(user_id, session_id)
	)`)
	if err != nil {
		log.Fatal(err)
	}

	// Dynamic migration for session_history: check if is_orphaned column exists (#303)
	ensureSessionHistoryColumns(db)

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

// ensureSessionHistoryColumns guarantees that is_orphaned column exists in session_history (#303).
func ensureSessionHistoryColumns(db *sql.DB) {
	if db == nil {
		return
	}
	var hasIsOrphaned bool
	rows, err := db.Query("PRAGMA table_info(session_history)")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, colType string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err == nil {
				if name == "is_orphaned" {
					hasIsOrphaned = true
					break
				}
			}
		}
	}
	if !hasIsOrphaned {
		_, _ = db.Exec("ALTER TABLE session_history ADD COLUMN is_orphaned BOOLEAN DEFAULT 0")
	}
}

// markSessionOrphaned flags a conversation session in session_history as orphaned due to context desync (#303).
func markSessionOrphaned(db *sql.DB, userID int64, sessionID string) {
	if db == nil || sessionID == "" {
		return
	}
	ensureSessionHistoryColumns(db)
	_, err := db.Exec("UPDATE session_history SET is_orphaned = 1 WHERE user_id = ? AND session_id = ?", userID, sessionID)
	if err != nil {
		log.Printf("DB Error marking session %s as orphaned for user %d: %v", sessionID, userID, err)
	}
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
