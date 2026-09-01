#!/bin/bash
set -e

if [ ! -f .env ]; then
    echo "CRITICAL: .env file not found! Create it with BOT_TOKENS and ADMIN_ID."
    exit 1
fi

echo "Building new engine..."
cd /root/projects/TheNovaNodes/antigravity-go-tg-bot-agent
go build -o new_engine .

echo "Creating systemd service..."
cat << 'SVC' > /etc/systemd/system/bot_new_engine.service
[Unit]
Description=New Go Bot Engine
After=network.target

[Service]
Type=simple
WorkingDirectory=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent
ExecStart=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent/new_engine
Environment="HOME=/root"
EnvironmentFile=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent/.env
Restart=always

[Install]
WantedBy=multi-user.target
SVC

echo "Reloading systemd..."
systemctl daemon-reload

echo "Starting services..."
systemctl enable --now bot_new_engine

echo "Status:"
systemctl status bot_new_engine --no-pager
