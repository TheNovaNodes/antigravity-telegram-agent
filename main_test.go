package main

import (
	"bytes"
	"engine/pkg/harvester"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

func TestIsValidModelID(t *testing.T) {
	valid := []string{
		"gemini-3.8-flash-high",
		"gemini-3.1-pro-high",
		"claude-sonnet-4-6",
		"claude-opus-4-6-thinking",
		"gpt-oss-120b-medium",
		"custom_model.1",
	}
	for _, id := range valid {
		if !isValidModelID(id) {
			t.Errorf("isValidModelID(%q) = false; want true", id)
		}
	}

	invalid := []string{
		"",
		"a",
		"Fetching",
		"fetching",
		"Error:",
		"Please",
		"⠋",
		"-gemini",
		"_model",
		".model",
		"gemini 3.8",
		"gemini/flash",
		"long" + strings.Repeat("x", 70),
	}
	for _, id := range invalid {
		if isValidModelID(id) {
			t.Errorf("isValidModelID(%q) = true; want false", id)
		}
	}
}

func TestParseModelsOutput_WithSpinnerAndANSI(t *testing.T) {
	rawOutput := "\r\xe2\xa0\x8b Fetching available models...\r\x1b[2K\r\xe2\xa0\x99 Fetching available models...\r\x1b[Kgemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
		"gemini-3.8-flash-medium\tGemini 3.8 Flash (Medium)\n" +
		"gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n" +
		"gemini-3.7-flash-high\tGemini 3.7 Flash (High)\n" +
		"gemini-3.7-flash-medium\tGemini 3.7 Flash (Medium)\n" +
		"gemini-3.7-flash-low\tGemini 3.7 Flash (Low)\n" +
		"gemini-3.6-flash-high\tGemini 3.6 Flash (High)\n" +
		"gemini-3.6-flash-medium\tGemini 3.6 Flash (Medium)\n" +
		"gemini-3.6-flash-low\tGemini 3.6 Flash (Low)\n" +
		"gemini-3.1-pro-high\tGemini 3.1 Pro (High)\n" +
		"gemini-3.1-pro-low\tGemini 3.1 Pro (Low)\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n" +
		"claude-opus-4-6-thinking\tClaude Opus 4.6 (Thinking)\n" +
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

	models := parseModelsOutput(rawOutput)
	if len(models) != 14 {
		t.Fatalf("parseModelsOutput returned %d models; want 14", len(models))
	}

	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), "fetching") || strings.Contains(strings.ToLower(m.Name), "fetching") {
			t.Errorf("Sanitizer leak: found fetching in model %+v", m)
		}
		if strings.Contains(m.ID, "\x1b") || strings.Contains(m.Name, "\x1b") {
			t.Errorf("ANSI leak in model %+v", m)
		}
	}

	if models[0].ID != "gemini-3.8-flash-high" || models[0].Emoji != "⚡" {
		t.Errorf("Unexpected first model: %+v", models[0])
	}
	if models[13].ID != "gpt-oss-120b-medium" || models[13].Emoji != "🟢" {
		t.Errorf("Unexpected last model: %+v", models[13])
	}
}

func TestParseModelsOutput_EdgeCases(t *testing.T) {
	// Empty input
	if res := parseModelsOutput(""); len(res) != 0 {
		t.Errorf("parseModelsOutput(\"\") = %d; want 0", len(res))
	}

	// Error message
	errMsg := "Error: Please sign in to view available models. Launch the CLI without arguments to sign in."
	if res := parseModelsOutput(errMsg); len(res) != 0 {
		t.Errorf("parseModelsOutput(errMsg) = %d; want 0", len(res))
	}

	// Duplicates and malformed lines
	dupInput := "gemini-3.8-flash-high Gemini Flash\n" +
		"gemini-3.8-flash-high Duplicate Flash\n" +
		"singleword\n" +
		"   \n" +
		"--- invalid\n"
	res := parseModelsOutput(dupInput)
	if len(res) != 1 {
		t.Fatalf("parseModelsOutput deduplication failed: got %d, want 1", len(res))
	}
	if res[0].Name != "Gemini Flash" {
		t.Errorf("Expected first instance preserved, got %q", res[0].Name)
	}
}

func TestGetPrimaryAccountHome(t *testing.T) {
	origPool := GlobalAccountPool
	origAccountsDir := os.Getenv("ACCOUNTS_DIR")
	origSystemHome := os.Getenv("SYSTEM_HOME")
	defer func() {
		GlobalAccountPool = origPool
		if origAccountsDir != "" {
			os.Setenv("ACCOUNTS_DIR", origAccountsDir)
		} else {
			os.Unsetenv("ACCOUNTS_DIR")
		}
		if origSystemHome != "" {
			os.Setenv("SYSTEM_HOME", origSystemHome)
		} else {
			os.Unsetenv("SYSTEM_HOME")
		}
	}()

	t.Run("Tier 1: GlobalAccountPool active account", func(t *testing.T) {
		poolDir := t.TempDir()
		accHome := filepath.Join(poolDir, "acc1")
		if err := os.MkdirAll(accHome, 0700); err != nil {
			t.Fatal(err)
		}
		pool, err := NewAccountPool(poolDir)
		if err != nil {
			t.Fatal(err)
		}
		pool.accounts["acc1"] = &Account{
			ID:      "acc1",
			HomeDir: accHome,
		}
		GlobalAccountPool = pool

		got := getPrimaryAccountHome()
		if got != accHome {
			t.Errorf("Tier 1 getPrimaryAccountHome() = %q; want %q", got, accHome)
		}
	})

	t.Run("Tier 2: Discovery via getAccountsDir with .gemini preference", func(t *testing.T) {
		GlobalAccountPool = nil
		baseDir := t.TempDir()
		os.Setenv("ACCOUNTS_DIR", baseDir)

		dirA := filepath.Join(baseDir, "dir_a")
		dirB := filepath.Join(baseDir, "dir_b")
		_ = os.MkdirAll(dirA, 0700)
		_ = os.MkdirAll(filepath.Join(dirB, ".gemini"), 0700)

		got := getPrimaryAccountHome()
		if got != dirB {
			t.Errorf("Tier 2 getPrimaryAccountHome() = %q; want %q (.gemini preferred)", got, dirB)
		}
	})

	t.Run("Tier 3: Fallback to getSystemBaseHome", func(t *testing.T) {
		GlobalAccountPool = nil
		emptyDir := t.TempDir()
		os.Setenv("ACCOUNTS_DIR", emptyDir)
		sysHome := t.TempDir()
		os.Setenv("SYSTEM_HOME", sysHome)

		got := getPrimaryAccountHome()
		if got != sysHome {
			t.Errorf("Tier 3 getPrimaryAccountHome() = %q; want %q", got, sysHome)
		}
	})
}

func TestFetchModels_RealExecutionWithAccountHome(t *testing.T) {
	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	envLog := filepath.Join(tmpDir, "env.log")

	script := fmt.Sprintf(`#!/bin/sh
echo "$HOME" > %q
echo "⠋ Fetching available models..."
echo "gemini-3.8-flash-high Google Gemini 3.8 Flash High"
echo "gemini-3.8-flash-medium Google Gemini 3.8 Flash Medium"
echo "claude-sonnet-4-6 Anthropic Claude 3.7 Sonnet"
`, envLog)

	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatalf("Failed to write mock: %v", err)
	}

	testHome := filepath.Join(tmpDir, "acc_test")
	if err := os.MkdirAll(filepath.Join(testHome, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}

	origPool := GlobalAccountPool
	origAccountsDir := os.Getenv("ACCOUNTS_DIR")
	origAgy := os.Getenv("AGY_BINARY")
	defer func() {
		GlobalAccountPool = origPool
		if origAccountsDir != "" {
			os.Setenv("ACCOUNTS_DIR", origAccountsDir)
		} else {
			os.Unsetenv("ACCOUNTS_DIR")
		}
		if origAgy != "" {
			os.Setenv("AGY_BINARY", origAgy)
		} else {
			os.Unsetenv("AGY_BINARY")
		}
	}()

	GlobalAccountPool = nil
	os.Setenv("ACCOUNTS_DIR", tmpDir)
	os.Setenv("AGY_BINARY", mockAgy)

	fetchModels()

	modelsMu.RLock()
	models := availableModels
	modelsMu.RUnlock()

	if len(models) != 3 {
		t.Fatalf("Expected 3 parsed models, got %d", len(models))
	}

	envBytes, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("Failed to read env log: %v", err)
	}
	recordedHome := strings.TrimSpace(string(envBytes))
	if recordedHome != testHome {
		t.Errorf("Mock agy received HOME=%q; want %q", recordedHome, testHome)
	}
}

func TestHandleModelCommand_FallbackAndDynamic(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	origModels := availableModels
	defer func() {
		modelsMu.Lock()
		availableModels = origModels
		modelsMu.Unlock()
	}()

	// 1. Fallback mode (len(availableModels) == 0)
	modelsMu.Lock()
	availableModels = nil
	modelsMu.Unlock()

	handleModelCommand(bot, 12345)

	ms.mu.Lock()
	body1 := ""
	if len(ms.sentBodies) > 0 {
		body1, _ = url.QueryUnescape(ms.sentBodies[len(ms.sentBodies)-1])
	}
	ms.mu.Unlock()

	if !strings.Contains(body1, "model:gemini-3.8-flash-high") {
		t.Errorf("Expected fallback model:gemini-3.8-flash-high, got body: %s", body1)
	}
	if !strings.Contains(body1, "model:gemini-3.8-flash-medium") {
		t.Errorf("Expected fallback model:gemini-3.8-flash-medium, got body: %s", body1)
	}
	if !strings.Contains(body1, "model:gemini-3.1-pro-high") {
		t.Errorf("Expected fallback model:gemini-3.1-pro-high, got body: %s", body1)
	}

	// 2. Dynamic mode (14 models)
	modelsMu.Lock()
	availableModels = []AgyModel{
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)", Emoji: "⚡"},
		{ID: "gemini-3.8-flash-medium", Name: "Gemini 3.8 Flash (Medium)", Emoji: "⚡"},
		{ID: "gemini-3.8-flash-low", Name: "Gemini 3.8 Flash (Low)", Emoji: "⚡"},
		{ID: "gemini-3.7-flash-high", Name: "Gemini 3.7 Flash (High)", Emoji: "⚡"},
		{ID: "gemini-3.7-flash-medium", Name: "Gemini 3.7 Flash (Medium)", Emoji: "⚡"},
		{ID: "gemini-3.7-flash-low", Name: "Gemini 3.7 Flash (Low)", Emoji: "⚡"},
		{ID: "gemini-3.6-flash-high", Name: "Gemini 3.6 Flash (High)", Emoji: "⚡"},
		{ID: "gemini-3.6-flash-medium", Name: "Gemini 3.6 Flash (Medium)", Emoji: "⚡"},
		{ID: "gemini-3.6-flash-low", Name: "Gemini 3.6 Flash (Low)", Emoji: "⚡"},
		{ID: "gemini-3.1-pro-high", Name: "Gemini 3.1 Pro (High)", Emoji: "🧠"},
		{ID: "gemini-3.1-pro-low", Name: "Gemini 3.1 Pro (Low)", Emoji: "🧠"},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6 (Thinking)", Emoji: "🟣"},
		{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 (Thinking)", Emoji: "🟣"},
		{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)", Emoji: "🟢"},
	}
	modelsMu.Unlock()

	handleModelCommand(bot, 12345)

	ms.mu.Lock()
	body2, _ := url.QueryUnescape(ms.sentBodies[len(ms.sentBodies)-1])
	ms.mu.Unlock()

	for _, m := range availableModels {
		expectedCallback := "model:" + m.ID
		if !strings.Contains(body2, expectedCallback) {
			t.Errorf("Expected dynamic model %s in keyboard, got body: %s", expectedCallback, body2)
		}
	}
}

func TestHandleRefreshModelsCommand(t *testing.T) {
	ms := newMockServer()
	defer ms.Close()
	bot := createMockBot(ms)

	tmpDir := t.TempDir()
	mockAgy := filepath.Join(tmpDir, "mock_agy.sh")
	script := `#!/bin/sh
echo "gemini-3.8-flash-high     Gemini 3.8 Flash (High)"
echo "gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)"
echo "gemini-3.8-flash-low      Gemini 3.8 Flash (Low)"
echo "gemini-3.7-flash-high     Gemini 3.7 Flash (High)"
echo "gemini-3.7-flash-medium   Gemini 3.7 Flash (Medium)"
echo "gemini-3.7-flash-low      Gemini 3.7 Flash (Low)"
echo "gemini-3.6-flash-high     Gemini 3.6 Flash (High)"
echo "gemini-3.6-flash-medium   Gemini 3.6 Flash (Medium)"
echo "gemini-3.6-flash-low      Gemini 3.6 Flash (Low)"
echo "gemini-3.1-pro-high       Gemini 3.1 Pro (High)"
echo "gemini-3.1-pro-low        Gemini 3.1 Pro (Low)"
echo "claude-sonnet-4-6         Claude Sonnet 4.6 (Thinking)"
echo "claude-opus-4-6-thinking  Claude Opus 4.6 (Thinking)"
echo "gpt-oss-120b-medium       GPT-OSS 120B (Medium)"
`
	if err := os.WriteFile(mockAgy, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	origAgy := os.Getenv("AGY_BINARY")
	origModels := availableModels
	defer func() {
		if origAgy != "" {
			os.Setenv("AGY_BINARY", origAgy)
		} else {
			os.Unsetenv("AGY_BINARY")
		}
		modelsMu.Lock()
		availableModels = origModels
		modelsMu.Unlock()
	}()

	os.Setenv("AGY_BINARY", mockAgy)

	handleRefreshModelsCommand(bot, 12345)

	ms.mu.Lock()
	body, _ := url.QueryUnescape(ms.sentBodies[len(ms.sentBodies)-1])
	ms.mu.Unlock()

	expectedMsg := "✅ Dynamically fetched 14 models from agy."
	if !strings.Contains(body, expectedMsg) {
		t.Errorf("Expected message %q, got body: %s", expectedMsg, body)
	}
}
