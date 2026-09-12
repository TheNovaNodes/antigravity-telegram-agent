#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

ENV_DIR="/etc/antigravity-bot"
PROD_ENV_FILE="${ENV_DIR}/env"

mkdir -p "${ENV_DIR}"
chmod 0700 "${ENV_DIR}"

if [ ! -f "${PROD_ENV_FILE}" ]; then
    if [ -f "${ROOT_DIR}/.env" ]; then
        echo "📋 Provisioning ${PROD_ENV_FILE} from ${ROOT_DIR}/.env (0600)..."
        cp "${ROOT_DIR}/.env" "${PROD_ENV_FILE}"
        chmod 0600 "${PROD_ENV_FILE}"
    else
        echo "❌ CRITICAL: No environment file found at ${PROD_ENV_FILE} or ${ROOT_DIR}/.env!"
        echo "Create ${PROD_ENV_FILE} with BOT_TOKENS and ALLOWED_ADMIN_IDS."
        exit 1
    fi
else
    chmod 0600 "${PROD_ENV_FILE}"
fi

echo "🔨 Building antigravity-bot-engine and agy-harvester..."
make all

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
EnvironmentFile=${PROD_ENV_FILE}
Environment="ENV_FILE=${PROD_ENV_FILE}"
Environment="HOME=${HOME:-/root}"
Environment="PATH=${PATH:-/usr/local/bin:/usr/bin:/bin}"
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
SVC

echo "🔄 Reloading systemd daemon..."
systemctl daemon-reload

echo "🚀 Restarting antigravity-bot-engine service..."
systemctl enable --now antigravity-bot-engine.service

echo "📊 Status:"
systemctl status antigravity-bot-engine.service --no-pager
