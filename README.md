# DMagyBOT 🤖

**DMagyBOT** — это высокопроизводительный асинхронный Telegram-бот для **Google Antigravity (`agy`)**, построенный на PTY-архитектуре с виртуальным терминалом **`pyte`**, встроенной поддержкой **MCP (Model Context Protocol)**, SQLite-сохранением состояния сессий, чанкингом сообщений, аудированием и интерактивным центром управления (Control Center).

---

## 🌟 Главные возможности
- **Model Context Protocol (MCP)**: Интеграция 3 ключевых служб:
  - 🧠 **AnythingLLM**: Семантическая память и база знаний (RAG).
  - 🔍 **SearXNG**: Приватный и глубокий веб-поиск.
  - 💼 **Nextcloud**: CRM пользователя, файлы, контакты, заметки и календарь.
- **PTY-Интеграция (`pexpect` + `pyte`)**: Работа с CLI-агентом `agy` без прямого API-ключа Gemini, используя системную OAuth-авторизацию сервера с чистым выводом ответа.
- **Интерактивный Control Center (`/menu` & `/mcp`)**: Наглядная панель с Inline-кнопками в Telegram для переключения моделей, режимов, уровней усилий (`effort`) и туггла MCP-серверов.
- **Персистентность сессий (SQLite)**: База данных `data/bot.db` сохраняет состояние `AgySession` (модель, режим, effort) и автоматически восстанавливает его при перезапусках.
- **Чанкинг ответов**: Автоматическое разделение ответов превышающих лимит Telegram в 4096 символов.
- **Аудит действий (`logs/audit.log`)**: JSON-логгер, фиксирующий запросы пользователей, Telegram ID, выбранные модели и время выполнения.
- **Автоматическая очистка ресурсов**: Фоновый процесс удаляет неактивные сессии (Idle > 30 мин) каждые 5 минут.
- **Полное тестовое покрытие**: Автоматический пакет unittest (24/24 пройденных тестов) и GitHub Actions CI workflow.

---

## 🏗️ Структура проекта
```
DMagyBOT/
├── .github/
│   └── workflows/
│       └── ci.yml          # GitHub Actions CI workflow
├── src/
│   ├── config.py           # Валидация переменных окружения и whitelist
│   ├── mcp_config.py       # Менеджер конфигурации MCP серверов (AnythingLLM, SearXNG, Nextcloud)
│   ├── mcp_manager.py      # Хэлпер статуса и управления MCP в Telegram
│   ├── cli_runner.py       # AgySession (PTY + pyte терминал, параметры моделей/режимов)
│   ├── session_manager.py  # SessionManager (управление сессиями и idle-очистка)
│   ├── db.py               # SQLite персистентность сессионных данных
│   ├── audit.py            # JSON-аудит логирование
│   ├── handlers.py         # Обработчики команд Telegram, callbacks и чанкинг
│   └── main.py             # Точка входа, фоновые сервисы и слэш-меню
├── tests/                  # Полный набор unittest-тестов
│   ├── test_audit.py
│   ├── test_chunking.py
│   ├── test_cli_runner.py
│   ├── test_config.py
│   ├── test_db_persistence.py
│   ├── test_handlers.py
│   ├── test_mcp.py
│   └── test_session_manager.py
├── data/                   # Хранилище SQLite базы данных (bot.db)
├── logs/                   # Журналы аудита (audit.log)
├── mcp_config.json         # Переменные и эндпоинты MCP серверов
├── dmagybot.service        # Unit-файл systemd
├── pyproject.toml          # Зависимости проекта
├── .env.example            # Шаблон конфигурации
└── README.md               # Документация
```

---

## 🛠️ Команды бота
- `/start` — Приветствие и краткое руководство.
- `/menu` — Открыть интерактивный Control Center Dashboard.
- `/mcp` — Открыть панель управления MCP серверами (AnythingLLM, SearXNG, Nextcloud).
- `/status` — Просмотр текущей конфигурации сессии и состояния системы.
- `/models` — Селектор модели через Inline-клавиатуру.
- `/effort` — Настройка уровня генерации и глубины рассуждений (`low`, `medium`, `high`).
- `/mode` — Выбор режима выполнения (`normal`, `yolo`, `safe`).
- `/reset` / `/clear` — Сброс текущей сессии диалога и очистка контекста.
- `/help` — Подробная справка по всем функциям бота.

---

## 🔌 Настройка MCP Серверов

MCP серверы конфигурируются в `mcp_config.json` или через переменные `.env`:

```env
# AnythingLLM (Semantic Memory)
ANYTHINGLLM_URL="http://localhost:3001"
ANYTHINGLLM_API_KEY="your_api_key"

# SearXNG (Web Search)
SEARXNG_URL="http://localhost:8080"

# Nextcloud (User CRM & Files)
NEXTCLOUD_URL="https://cloud.example.com"
NEXTCLOUD_USER="username"
NEXTCLOUD_PASS="app_password"
```

---

## 🧪 Запуск тестов

Для запуска тестового пакета используйте:
```bash
.venv/bin/python -m unittest discover -s tests
```

---

## 🚀 Быстрый старт

### 1. Установка окружения
```bash
python3.12 -m venv .venv
source .venv/bin/activate
pip install aiogram pexpect pyte python-dotenv
```

### 2. Настройка `.env`
Скопируйте пример конфига и заполните токены:
```bash
cp .env.example .env
chmod 600 .env
```

### 3. Деплой через systemd
```bash
cp dmagybot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now dmagybot
```
