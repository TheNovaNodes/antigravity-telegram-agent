#!/bin/bash
set -e

if [ ! -f .env ]; then
    echo "CRITICAL: .env file not found! Create it with ALL_TOKENS and ADMIN_ID."
    exit 1
fi

source .env

echo "Stopping legacy engine..."
systemctl stop bot_old_engine || true
systemctl disable bot_old_engine || true

echo "Updating bot_new_engine.service..."
cat << 'SVC' > /etc/systemd/system/bot_new_engine.service
[Unit]
Description=Antigravity Go Bot Engine (All Bots)
After=network.target

[Service]
Type=simple
WorkingDirectory=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent
ExecStart=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent/new_engine
EnvironmentFile=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent/.env
Environment="HOME=/root"
Environment="PATH=/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/snap/bin"
Restart=always

[Install]
WantedBy=multi-user.target
SVC

echo "Reloading and restarting..."
systemctl daemon-reload
systemctl restart bot_new_engine

echo "Cleaning up old service file..."
rm -f /etc/systemd/system/bot_old_engine.service
systemctl daemon-reload

echo "Status of the new engine:"
systemctl status bot_new_engine --no-pager
