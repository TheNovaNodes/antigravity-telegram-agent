package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
