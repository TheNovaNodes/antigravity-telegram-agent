package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitDB_HardeningAndMigration(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	botName := "HardenedBot"
	db := initDB(botName)
	if db == nil {
		t.Fatal("Expected db instance, got nil")
	}

	// Verify users and session_history tables exist
	var userTable, historyTable string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='users'").Scan(&userTable)
	if err != nil || userTable != "users" {
		t.Fatalf("Table 'users' missing: %v", err)
	}

	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='session_history'").Scan(&historyTable)
	if err != nil || historyTable != "session_history" {
		t.Fatalf("Table 'session_history' missing: %v", err)
	}

	// Verify voice_reply column exists in users
	var hasVoiceReply bool
	rows, err := db.Query("PRAGMA table_info(users)")
	if err != nil {
		t.Fatalf("Failed to query table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err == nil {
			if name == "voice_reply" {
				hasVoiceReply = true
			}
		}
	}
	rows.Close()

	if !hasVoiceReply {
		t.Errorf("Expected 'voice_reply' column in table 'users'")
	}

	db.Close()

	// Simulate restart on existing database: should not error or panic
	db2 := initDB(botName)
	if db2 == nil {
		t.Fatal("Expected second initDB instance on existing DB, got nil")
	}
	db2.Close()

	dbFile := filepath.Join(tempDir, "sessions_"+botName+".db")
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		t.Fatalf("Database file was not created at expected path: %s", dbFile)
	}
}
