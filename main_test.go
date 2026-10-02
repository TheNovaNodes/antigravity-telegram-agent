package main

import (
	"bytes"
	"engine/pkg/harvester"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestStartMetricsServer_DisabledWhenEmpty(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	t.Setenv("METRICS_PORT", "")

	srv, err := StartMetricsServer("")
	if err != nil {
		t.Fatalf("Expected nil error for empty addr, got: %v", err)
	}
	if srv != nil {
		t.Errorf("Expected nil server when no address specified")
	}
}

func TestStartMetricsServer_HealthzAndPrometheus(t *testing.T) {
	// Allocate free ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to bind ephemeral port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	srv, err := StartMetricsServer(addr)
	if err != nil {
		t.Fatalf("StartMetricsServer failed: %v", err)
	}
	if srv == nil {
		t.Fatalf("Expected non-nil server")
	}
	defer func() {
		_ = StopMetricsServer(srv)
	}()

	// Allow server goroutine to start listening
	time.Sleep(100 * time.Millisecond)

	// 1. Check /healthz
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("Failed to request /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 from /healthz, got: %d", resp.StatusCode)
	}

	// 2. Trigger harvester metric recording
	harvester.RecordArtifact(harvester.ExtractedArtifact{
		Kind:          harvester.KindADR,
		RedactedCount: 3,
	})
	harvester.SetOrphanCount(7)

	// 3. Check /metrics
	metricsResp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("Failed to request /metrics: %v", err)
	}
	defer metricsResp.Body.Close()

	bodyBytes, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("Failed to read /metrics body: %v", err)
	}
	body := string(bodyBytes)

	if !strings.Contains(body, "agy_harvester_extracted_total") {
		t.Errorf("Expected agy_harvester_extracted_total in /metrics output, got: %s", body)
	}
	if !strings.Contains(body, "agy_harvester_sanitized_secrets_total") {
		t.Errorf("Expected agy_harvester_sanitized_secrets_total in /metrics output, got: %s", body)
	}
	if !strings.Contains(body, "agy_harvester_orphan_uncommitted_count") {
		t.Errorf("Expected agy_harvester_orphan_uncommitted_count in /metrics output, got: %s", body)
	}
}

func TestGetEmojiForModel(t *testing.T) {
	tests := []struct {
		modelID  string
		expected string
	}{
		{"gemini-3.7-flash-high", "⚡"},
		{"gemini-3.8-flash-high", "⚡"},
		{"gemini-3.1-pro-high", "🧠"},
		{"gemini-3.1-pro-low", "🧠"},
		{"claude-sonnet-4-6", "🟣"},
		{"claude-opus-4-6", "🟣"},
		{"gpt-oss-120b", "🟢"},
		{"llama-3-70b", "🟢"},
		{"custom-gpt-4o", "🤖"},
	}

	for _, tc := range tests {
		got := getEmojiForModel(tc.modelID)
		if got != tc.expected {
			t.Errorf("getEmojiForModel(%q) = %q; want %q", tc.modelID, got, tc.expected)
		}
	}
}

func TestFetchModels(t *testing.T) {
	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := `#!/bin/sh
echo "gemini-3.8-flash-high Google Gemini 3.8 Flash High"
echo "claude-sonnet-4-6 Anthropic Claude 3.7 Sonnet"
echo ""
`
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to create mock agy script: %v", err)
	}

	os.Setenv("AGY_BINARY", mockAgy)
	defer os.Unsetenv("AGY_BINARY")

	fetchModels()

	modelsMu.Lock()
	models := availableModels
	modelsMu.Unlock()

	if len(models) < 2 {
		t.Fatalf("Expected at least 2 models, got %d", len(models))
	}

	if models[0].ID != "gemini-3.8-flash-high" || models[0].Emoji != "⚡" {
		t.Errorf("Unexpected model 0: %+v", models[0])
	}

	if models[1].ID != "claude-sonnet-4-6" || models[1].Emoji != "🟣" {
		t.Errorf("Unexpected model 1: %+v", models[1])
	}
}

func TestFetchModels_InvalidBinary(t *testing.T) {
	os.Setenv("AGY_BINARY", "/nonexistent/path/agy")
	defer os.Unsetenv("AGY_BINARY")

	// Should not crash, simply logs error
	fetchModels()
}

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
