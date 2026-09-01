#!/bin/bash
set -e

echo "Building new engine..."
cd /root/projects/TheNovaNodes/antigravity-go-tg-bot-agent
go build -o new_engine .

echo "Copying databases without data loss..."
cp /root/projects/TheNovaNodes/antigravity-cli-telegram-bot/sessions_*.db .

OLD_TOKENS="***,***,***,***"
NEW_TOKENS="***,***,***,***"
ADMIN_ID="173681771"

echo "Creating systemd services..."
cat << 'SVC' > /etc/systemd/system/bot_old_engine.service
[Unit]
Description=Legacy Bot Engine (4 Bots)
After=network.target

[Service]
Type=simple
WorkingDirectory=/root/projects/TheNovaNodes/antigravity-cli-telegram-bot
ExecStart=/root/projects/TheNovaNodes/antigravity-cli-telegram-bot/pure_router
Environment="BOT_TOKENS=${OLD_TOKENS}"
Environment="ALLOWED_ADMIN_IDS=${ADMIN_ID}"
Restart=always

[Install]
WantedBy=multi-user.target
SVC
sed -i "s/\${OLD_TOKENS}/$OLD_TOKENS/g" /etc/systemd/system/bot_old_engine.service
sed -i "s/\${ADMIN_ID}/$ADMIN_ID/g" /etc/systemd/system/bot_old_engine.service

cat << 'SVC' > /etc/systemd/system/bot_new_engine.service
[Unit]
Description=New Go Bot Engine (4 Migrated Bots)
After=network.target

[Service]
Type=simple
WorkingDirectory=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent
ExecStart=/root/projects/TheNovaNodes/antigravity-go-tg-bot-agent/new_engine
Environment="HOME=/root"
Environment="BOT_TOKENS=${NEW_TOKENS}"
Environment="ALLOWED_ADMIN_IDS=${ADMIN_ID}"
Restart=always

[Install]
WantedBy=multi-user.target
SVC
sed -i "s/\${NEW_TOKENS}/$NEW_TOKENS/g" /etc/systemd/system/bot_new_engine.service
sed -i "s/\${ADMIN_ID}/$ADMIN_ID/g" /etc/systemd/system/bot_new_engine.service

echo "Reloading systemd..."
systemctl daemon-reload

echo "Killing old zombie processes..."
pkill -f pure_router || true

echo "Starting services..."
systemctl enable --now bot_old_engine
systemctl enable --now bot_new_engine

echo "Status:"
systemctl status bot_old_engine --no-pager
systemctl status bot_new_engine --no-pager
