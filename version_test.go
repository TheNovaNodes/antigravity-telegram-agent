package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHandleVersionFlag(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	// Test no args
	os.Args = []string{"antigravity-bot-engine"}
	if handleVersionFlag() {
		t.Errorf("handleVersionFlag() should return false when no flags are passed")
	}

	// Test --version
	os.Args = []string{"antigravity-bot-engine", "--version"}
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	handled := handleVersionFlag()

	_ = w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	if !handled {
		t.Errorf("handleVersionFlag() should return true for --version")
	}
	if !strings.Contains(output, "antigravity-bot-engine version") {
		t.Errorf("Expected version string in output, got: %s", output)
	}

	// Test unknown flag
	os.Args = []string{"antigravity-bot-engine", "--unknown"}
	if handleVersionFlag() {
		t.Errorf("handleVersionFlag() should return false for unknown flags")
	}
}
