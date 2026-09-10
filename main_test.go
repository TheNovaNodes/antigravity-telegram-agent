package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// If AGY_BINARY is not set and default agy path does not exist on host (e.g. CI runner),
	// provision a mock executable so unit tests spinning up sessions can run cleanly.
	defaultPath := getAgyPath()
	var cleanup func()
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		tmpDir, err := os.MkdirTemp("", "agy_mock_*")
		if err == nil {
			mockBin := filepath.Join(tmpDir, "mock_agy.sh")
			script := "#!/bin/sh\nexec sleep 30\n"
			if err := os.WriteFile(mockBin, []byte(script), 0755); err == nil {
				fallbackAgyBinary = mockBin
				if os.Getenv("AGY_BINARY") == "" {
					os.Setenv("AGY_BINARY", mockBin)
				}
				cleanup = func() { _ = os.RemoveAll(tmpDir) }
			}
		}
	}

	code := m.Run()
	if cleanup != nil {
		cleanup()
	}
	os.Exit(code)
}
