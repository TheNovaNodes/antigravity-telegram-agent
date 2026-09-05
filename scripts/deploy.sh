#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

if [ ! -f .env ]; then
    echo "❌ CRITICAL: .env file not found in ${ROOT_DIR}! Create it with BOT_TOKENS and ALLOWED_ADMIN_IDS."
    exit 1
fi

echo "🔨 Building antigravity-bot-engine..."
make build

SERVICE_FILE="/etc/systemd/system/antigravity-bot-engine.service"

echo "⚙️ Installing systemd unit: ${SERVICE_FILE}..."
cat << SVC > "${SERVICE_FILE}"
[Unit]
Description=Antigravity Go Telegram Bot Engine (Multi-Agent Swarm)
After=network.target
StartLimitIntervalSec=60s
StartLimitBurst=5

[Service]
Type=simple
WorkingDirectory=${ROOT_DIR}
ExecStart=${ROOT_DIR}/bin/antigravity-bot-engine
EnvironmentFile=${ROOT_DIR}/.env
Environment="HOME=/root"
Environment="PATH=/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/snap/bin"
Restart=always
RestartSec=3

# Sandboxing & Hardening (#197)
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
InaccessiblePaths=-/root/.ssh -/root/.gnupg

[Install]
WantedBy=multi-user.target
SVC

echo "🔄 Reloading systemd daemon..."
systemctl daemon-reload

echo "🚀 Restarting antigravity-bot-engine service..."
systemctl enable --now antigravity-bot-engine.service

echo "📊 Status:"
systemctl status antigravity-bot-engine.service --no-pager
