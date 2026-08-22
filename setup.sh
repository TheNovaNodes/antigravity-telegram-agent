#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# AntigravityTelegramAgent — Universal Installer
# Supports interactive mode (no args) and automation (--token / --user-id)
# ─────────────────────────────────────────────────────────────────────────────

BOLD="\033[1m"
GREEN="\033[0;32m"
YELLOW="\033[0;33m"
RED="\033[0;31m"
CYAN="\033[0;36m"
RESET="\033[0m"

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVICE_NAME="antigravity-telegram-agent"
VENV_DIR="${PROJECT_DIR}/.venv"
ENV_FILE="${PROJECT_DIR}/.env"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# ─────────────────── Helpers ───────────────────────────────────────────────
info()    { echo -e "${GREEN}✅ $1${RESET}"; }
warn()    { echo -e "${YELLOW}⚠️  $1${RESET}"; }
error()   { echo -e "${RED}❌ $1${RESET}"; exit 1; }
header()  { echo -e "\n${CYAN}${BOLD}$1${RESET}\n"; }

# ─────────────────── Parse CLI Arguments ───────────────────────────────────
BOT_TOKEN=""
USER_IDS=""
AGY_PATH_ARG=""
VAULT_REF=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --token=*)      BOT_TOKEN="${1#*=}"; shift ;;
        --token)        BOT_TOKEN="$2"; shift 2 ;;
        --user-id=*)    USER_IDS="${1#*=}"; shift ;;
        --user-id)      USER_IDS="$2"; shift 2 ;;
        --agy-path=*)   AGY_PATH_ARG="${1#*=}"; shift ;;
        --agy-path)     AGY_PATH_ARG="$2"; shift 2 ;;
        --vault-ref=*)  VAULT_REF="${1#*=}"; shift ;;
        --vault-ref)    VAULT_REF="$2"; shift 2 ;;
        --help|-h)
            echo "Usage: ./setup.sh [--token=BOT_TOKEN] [--user-id=TELEGRAM_USER_ID] [--agy-path=/path/to/agy] [--vault-ref=POINTER]"
            echo ""
            echo "  --token      Telegram Bot Token from @BotFather (or vault pointer)"
            echo "  --user-id    Comma-separated list of allowed Telegram user IDs"
            echo "  --agy-path   Path to agy binary (auto-detected if omitted)"
            echo "  --vault-ref  Vault reference pointer (e.g. vault:ref:XYZ) for Vault-first secret architecture"
            echo ""
            echo "If arguments are omitted, the installer will ask interactively."
            exit 0
            ;;
        *) warn "Unknown argument: $1"; shift ;;
    esac
done

# ─────────────────── Banner ────────────────────────────────────────────────
clear
echo -e "${BOLD}${CYAN}"
cat << 'BANNER'

    ██████╗ ███╗   ███╗ █████╗  ██████╗██╗   ██╗██████╗  ██████╗ ████████╗
    ██╔══██╗████╗ ████║██╔══██╗██╔════╝╚██╗ ██╔╝██╔══██╗██╔═══██╗╚══██╔══╝
    ██║  ██║██╔████╔██║███████║██║  ███╗╚████╔╝ ██████╔╝██║   ██║   ██║
    ██║  ██║██║╚██╔╝██║██╔══██║██║   ██║ ╚██╔╝  ██╔══██╗██║   ██║   ██║
    ██████╔╝██║ ╚═╝ ██║██║  ██║╚██████╔╝  ██║   ██████╔╝╚██████╔╝   ██║
    ╚═════╝ ╚═╝     ╚═╝╚═╝  ╚═╝ ╚═════╝   ╚═╝   ╚═════╝  ╚═════╝    ╚═╝

BANNER
echo -e "${RESET}"
echo -e "${BOLD}    Telegram Bridge for Google Antigravity CLI${RESET}"
echo -e "    Installer v1.0"
echo ""

# ─────────────────── Step 1: Check Prerequisites ──────────────────────────
header "Шаг 1 из 5: Проверка зависимостей"

# Check Python 3.10+
if ! command -v python3 &>/dev/null; then
    error "Python 3 не найден. Установите Python 3.10+ и повторите."
fi
PY_VERSION=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
info "Python ${PY_VERSION} найден"

# Check agy CLI — resolve real user's HOME when running under sudo
REAL_USER="${SUDO_USER:-$(whoami)}"
REAL_HOME=$(eval echo "~${REAL_USER}")

AGY_PATH="${AGY_PATH_ARG:-""}"

if [[ -z "$AGY_PATH" ]]; then
    for candidate in \
        "$(command -v agy 2>/dev/null)" \
        "${REAL_HOME}/.local/bin/agy" \
        "$HOME/.local/bin/agy" \
        "/usr/local/bin/agy" \
        "/root/.local/bin/agy" \
        "/home/${REAL_USER}/.local/bin/agy"; do
        if [[ -n "$candidate" && -f "$candidate" ]]; then
            AGY_PATH="$candidate"
            break
        fi
    done
fi

# Fallback: search the entire system
if [[ -z "$AGY_PATH" ]]; then
    warn "agy не найден в стандартных путях, ищу по всей системе..."
    AGY_PATH="$(find / -name agy -type f -executable 2>/dev/null | head -1 || true)"
fi

if [[ -z "$AGY_PATH" ]]; then
    error "Antigravity CLI (agy) не найден!\n\n   Сначала установите agy и авторизуйтесь:\n   curl -sS https://dl.google.com/agy/install.sh | bash\n   agy auth login\n\n   Или укажите путь вручную:\n   ./setup.sh --agy-path=/path/to/agy"
fi
info "Antigravity CLI найден: ${AGY_PATH}"

# Check Vault-first secret architecture components
if [[ -x "/usr/local/bin/with-secret" ]]; then
    info "Vault execution wrapper found: /usr/local/bin/with-secret"
else
    warn "Vault wrapper /usr/local/bin/with-secret not found (Vault-first execution recommended)"
fi

if ss -tulpn 2>/dev/null | grep -q ":8301"; then
    info "Agent Vault service active on port 8301"
elif curl -s http://127.0.0.1:8301/health &>/dev/null || nc -z 127.0.0.1 8301 &>/dev/null; then
    info "Agent Vault service active on port 8301"
else
    warn "Agent Vault service not detected on port 8301"
fi

# Check agy auth — look in real user's home first
TOKEN_FILE=""
for tf in "${REAL_HOME}/.gemini/antigravity-cli/antigravity-oauth-token" \
          "$HOME/.gemini/antigravity-cli/antigravity-oauth-token"; do
    if [[ -f "$tf" ]]; then
        TOKEN_FILE="$tf"
        break
    fi
done

if [[ -z "$TOKEN_FILE" ]]; then
    error "Вы не авторизованы в Antigravity CLI!\n\n   Выполните: agy auth login"
fi
info "Авторизация Antigravity CLI подтверждена (${REAL_USER})"

# ─────────────────── Step 2: Bot Token & User ID ──────────────────────────
header "Шаг 2 из 5: Настройка Telegram"

if [[ -z "$BOT_TOKEN" ]]; then
    echo -e "${BOLD}Введите токен Telegram бота${RESET}"
    echo -e "  (Получить у @BotFather → /newbot)"
    echo ""
    read -rp "  🔑 Bot Token: " BOT_TOKEN
    echo ""
fi

if [[ -z "$BOT_TOKEN" ]]; then
    error "Токен бота не может быть пустым!"
fi

if [[ -n "$VAULT_REF" ]]; then
    BOT_TOKEN="$VAULT_REF"
    info "Vault pointer reference applied for TELEGRAM_BOT_TOKEN: ${VAULT_REF}"
fi

# Validate token format (rough check: contains ':' or starts with 'vault:ref:')
if [[ "$BOT_TOKEN" != *":"* && "$BOT_TOKEN" != vault:ref:* ]]; then
    warn "Токен не содержит ':' и не является pointer-ссылкой Vault — проверьте правильность формата."
fi

info "Токен бота принят"

if [[ -z "$USER_IDS" ]]; then
    echo -e "${BOLD}Введите ваш Telegram User ID${RESET}"
    echo -e "  (Узнать у @userinfobot или @getmyid_bot)"
    echo -e "  (Можно указать несколько через запятую: 123,456)"
    echo ""
    read -rp "  👤 User ID: " USER_IDS
    echo ""
fi

if [[ -z "$USER_IDS" ]]; then
    error "User ID не может быть пустым!"
fi

info "User ID принят: ${USER_IDS}"

# ─────────────────── Step 3: Create .env ──────────────────────────────────
header "Шаг 3 из 5: Создание конфигурации (Vault-First Guidelines Enforced)"

VAULT_COMMENT=""
if [[ "$BOT_TOKEN" == vault:ref:* ]]; then
    VAULT_COMMENT="# Vault Architecture active: Secrets resolved at runtime via agent-vault on port 8301 / with-secret wrapper\n"
fi

cat > "$ENV_FILE" << EOF
# AntigravityTelegramAgent Configuration (auto-generated by setup.sh)
$(echo -e "$VAULT_COMMENT")TELEGRAM_BOT_TOKEN="${BOT_TOKEN}"
ALLOWED_USER_IDS="${USER_IDS}"
LOG_LEVEL="INFO"
AGY_BINARY_PATH="${AGY_PATH}"
EOF
chown "${REAL_USER}:${REAL_USER}" "$ENV_FILE"
chmod 0600 "$ENV_FILE"
info "Файл .env создан и защищен (права пользователя ${REAL_USER}, 0600)"

# ─────────────────── Step 4: Python Virtual Environment ───────────────────
header "Шаг 4 из 5: Установка зависимостей"

if [[ ! -d "$VENV_DIR" ]]; then
    python3 -m venv "$VENV_DIR"
    info "Виртуальное окружение создано: ${VENV_DIR}"
else
    info "Виртуальное окружение уже существует"
fi

chown -R "${REAL_USER}:${REAL_USER}" "$PROJECT_DIR"
"${VENV_DIR}/bin/pip" install --upgrade pip
echo ""
echo -e "${YELLOW}  Устанавливаю зависимости (это может занять 1-2 минуты)...${RESET}"
echo ""
"${VENV_DIR}/bin/pip" install aiogram pexpect pyte python-dotenv
echo ""
info "Все зависимости установлены"

# ─────────────────── Step 5: Systemd Service ──────────────────────────────
header "Шаг 5 из 5: Настройка автозапуска (systemd)"

cat > "$SERVICE_FILE" << EOF
[Unit]
Description=AntigravityTelegramAgent - Telegram Bridge for Antigravity AGY
After=network.target

[Service]
Type=simple
User=${REAL_USER}
WorkingDirectory=${PROJECT_DIR}
Environment="PYTHONPATH=${PROJECT_DIR}"
Environment="HOME=${REAL_HOME}"
ExecStart=${VENV_DIR}/bin/python src/main.py
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

info "Systemd-сервис создан: ${SERVICE_FILE}"

# Fix project directory permissions for REAL_USER
chown -R "${REAL_USER}:${REAL_USER}" "$PROJECT_DIR"
chmod 600 "$ENV_FILE"

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}" --quiet
systemctl restart "${SERVICE_NAME}"

info "Сервис ${SERVICE_NAME} запущен и добавлен в автозапуск"

# ─────────────────── Done! ────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}══════════════════════════════════════════════════════${RESET}"
echo -e "${GREEN}${BOLD}  🚀 AntigravityTelegramAgent успешно установлен и запущен!${RESET}"
echo -e "${GREEN}${BOLD}══════════════════════════════════════════════════════${RESET}"
echo ""
echo -e "  ${BOLD}Что дальше:${RESET}"
echo -e "  1. Откройте Telegram и найдите вашего бота"
echo -e "  2. Отправьте ${BOLD}/start${RESET} для начала работы"
echo -e "  3. Отправьте ${BOLD}/menu${RESET} для панели управления"
echo ""
echo -e "  ${BOLD}Полезные команды:${RESET}"
echo -e "  • Статус бота:   ${CYAN}systemctl status ${SERVICE_NAME}${RESET}"
echo -e "  • Логи бота:     ${CYAN}journalctl -u ${SERVICE_NAME} -f${RESET}"
echo -e "  • Перезапуск:    ${CYAN}systemctl restart ${SERVICE_NAME}${RESET}"
echo -e "  • Остановка:     ${CYAN}systemctl stop ${SERVICE_NAME}${RESET}"
echo ""
echo -e "  ${BOLD}Документация:${RESET} ${CYAN}cat README.md${RESET}"
echo ""
