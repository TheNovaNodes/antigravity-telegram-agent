package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetDataDir_EnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	customDataDir := filepath.Join(tmpDir, "custom_data")

	t.Setenv("DATA_DIR", customDataDir)

	resolved := getDataDir()
	if resolved != customDataDir {
		t.Fatalf("Expected getDataDir() to return %s, got %s", customDataDir, resolved)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatalf("Failed to stat resolved data dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("Resolved path %s is not a directory", resolved)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("Expected directory permissions 0700, got %04o", perm)
	}
}

func TestGetDataDir_Fallback(t *testing.T) {
	t.Setenv("DATA_DIR", "")

	resolved := getDataDir()
	if resolved == "" {
		t.Fatalf("Expected non-empty resolved data directory")
	}

	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatalf("Resolved data dir %s cannot be accessed: %v", resolved, err)
	}
	if !info.IsDir() {
		t.Fatalf("Resolved path %s is not a directory", resolved)
	}
}

func TestMigrateDataFiles_LifecycleAndPermissions(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "target_store")

	// 1. Prepare legacy mock files
	dbFile := filepath.Join(srcDir, "sessions_bot1.db")
	walFile := filepath.Join(srcDir, "sessions_bot1.db-wal")
	shmFile := filepath.Join(srcDir, "sessions_bot1.db-shm")
	ignoredFile := filepath.Join(srcDir, "random.log")
	ignoredSubdir := filepath.Join(srcDir, "sessions_subdir")

	_ = os.WriteFile(dbFile, []byte("sqlite database content"), 0644)
	_ = os.WriteFile(walFile, []byte("wal journal content"), 0644)
	_ = os.WriteFile(shmFile, []byte("shm index content"), 0644)
	_ = os.WriteFile(ignoredFile, []byte("log data"), 0644)
	_ = os.Mkdir(ignoredSubdir, 0755)

	// 2. Perform Migration
	count, err := MigrateDataFiles(srcDir, dstDir)
	if err != nil {
		t.Fatalf("MigrateDataFiles failed: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 migrated files, got %d", count)
	}

	// 3. Verify destination files and 0600 permissions
	for _, name := range []string{"sessions_bot1.db", "sessions_bot1.db-wal", "sessions_bot1.db-shm"} {
		targetPath := filepath.Join(dstDir, name)
		info, err := os.Stat(targetPath)
		if err != nil {
			t.Errorf("Migrated file %s not found in destination: %v", name, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("Expected file %s to have 0600 permissions, got %04o", name, perm)
		}
	}

	// 4. Verify non-db files were not copied
	if _, err := os.Stat(filepath.Join(dstDir, "random.log")); !os.IsNotExist(err) {
		t.Errorf("Expected random.log to be skipped during migration")
	}

	// 5. Test idempotency (should not re-copy or overwrite existing files)
	countSecondRun, err := MigrateDataFiles(srcDir, dstDir)
	if err != nil {
		t.Fatalf("Second MigrateDataFiles run failed: %v", err)
	}
	if countSecondRun != 0 {
		t.Errorf("Expected 0 files migrated on second run (idempotent), got %d", countSecondRun)
	}

	// 6. Test identical source and destination directories
	sameCount, err := MigrateDataFiles(dstDir, dstDir)
	if err != nil {
		t.Fatalf("MigrateDataFiles with same src and dst failed: %v", err)
	}
	if sameCount != 0 {
		t.Errorf("Expected 0 files migrated when src == dst, got %d", sameCount)
	}

	// 7. Test non-existent source directory
	nonExistentSrc := filepath.Join(t.TempDir(), "does_not_exist")
	noCount, err := MigrateDataFiles(nonExistentSrc, dstDir)
	if err != nil {
		t.Fatalf("MigrateDataFiles with non-existent src returned error: %v", err)
	}
	if noCount != 0 {
		t.Errorf("Expected 0 files migrated from non-existent src, got %d", noCount)
	}
}

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
	if info, err := os.Stat(dbFile); os.IsNotExist(err) {
		t.Fatalf("Database file was not created at expected path: %s", dbFile)
	} else if err == nil {
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Errorf("Expected db file permissions to be 0600, got %o", mode)
		}
	}
}

func TestInitDB_Enforces0600PermissionsOnExistingFile(t *testing.T) {
	tempDir := t.TempDir()
	os.Setenv("DATA_DIR", tempDir)
	defer os.Unsetenv("DATA_DIR")

	botName := "PermTestBot"
	dbFile := filepath.Join(tempDir, "sessions_"+botName+".db")

	// Pre-create database file with permissive 0644 mode
	if err := os.WriteFile(dbFile, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to pre-create db file: %v", err)
	}

	db := initDB(botName)
	if db == nil {
		t.Fatal("Expected db instance, got nil")
	}
	db.Close()

	info, err := os.Stat(dbFile)
	if err != nil {
		t.Fatalf("Failed to stat db file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("Expected db file permissions to be 0600, got %o", mode)
	}
}
