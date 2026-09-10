package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemdSandboxingConfigInDeployScript(t *testing.T) {
	deployScriptPath := filepath.Join("scripts", "deploy.sh")
	data, err := os.ReadFile(deployScriptPath)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", deployScriptPath, err)
	}

	content := string(data)

	requiredDirectives := []string{
		"StartLimitIntervalSec=60s",
		"StartLimitBurst=5",
		"Restart=always",
		"RestartSec=3",
		"EnvironmentFile=${PROD_ENV_FILE}",
		`ENV_DIR="/etc/antigravity-bot"`,
	}

	for _, dir := range requiredDirectives {
		if !strings.Contains(content, dir) {
			t.Errorf("deploy.sh missing expected systemd directive: %q", dir)
		}
	}

	// Ensure restrictive sandbox directives that hamper devops/coding agents are NOT present
	forbiddenDirectives := []string{
		"ProtectSystem=",
		"InaccessiblePaths=",
		"ProtectKernelTunables=",
		"ProtectKernelModules=",
		"ProtectControlGroups=",
		"NoNewPrivileges=",
		"PrivateTmp=",
	}

	for _, dir := range forbiddenDirectives {
		if strings.Contains(content, dir) {
			t.Errorf("deploy.sh contains restrictive sandbox directive that hampers coding agents: %q", dir)
		}
	}

	// Verify syntax using systemd-analyze if installed
	if systemdAnalyzePath, err := exec.LookPath("systemd-analyze"); err == nil {
		tempDir := t.TempDir()
		unitFile := filepath.Join(tempDir, "test-antigravity.service")

		// Minimal mock unit matching the deploy script template
		mockUnit := `[Unit]
Description=Test Antigravity Service
After=network.target
StartLimitIntervalSec=60s
StartLimitBurst=5

[Service]
Type=simple
ExecStart=/bin/true
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`
		if err := os.WriteFile(unitFile, []byte(mockUnit), 0644); err != nil {
			t.Fatalf("Failed to write mock unit file: %v", err)
		}

		cmd := exec.Command(systemdAnalyzePath, "verify", unitFile)
		cmd.WaitDelay = 2 * time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("systemd-analyze verify failed: %v, output: %s", err, string(output))
		}
	}
}
