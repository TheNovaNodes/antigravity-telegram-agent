package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
		"NoNewPrivileges=yes",
		"PrivateTmp=yes",
		"ProtectSystem=full",
		"ProtectKernelTunables=yes",
		"ProtectKernelModules=yes",
		"ProtectControlGroups=yes",
		"InaccessiblePaths=-/root/.ssh -/root/.gnupg",
		"EnvironmentFile=${PROD_ENV_FILE}",
		`ENV_DIR="/etc/antigravity-bot"`,
	}

	for _, dir := range requiredDirectives {
		if !strings.Contains(content, dir) {
			t.Errorf("deploy.sh missing expected systemd directive: %q", dir)
		}
	}

	// Verify syntax using systemd-analyze if installed
	if systemdAnalyzePath, err := exec.LookPath("systemd-analyze"); err == nil {
		tempDir := t.TempDir()
		unitFile := filepath.Join(tempDir, "test-antigravity.service")

		// Minimal mock unit containing the directives
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
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
InaccessiblePaths=-/root/.ssh -/root/.gnupg

[Install]
WantedBy=multi-user.target
`
		if err := os.WriteFile(unitFile, []byte(mockUnit), 0644); err != nil {
			t.Fatalf("Failed to write mock unit file: %v", err)
		}

		cmd := exec.Command(systemdAnalyzePath, "verify", unitFile)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("systemd-analyze verify failed: %v, output: %s", err, string(output))
		}
	}
}
