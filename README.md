# DMagyBOT 🤖

**DMagyBOT** — это высокопроизводительный асинхронный Telegram-бот для **Google Antigravity (`agy`)**, построенный на PTY-архитектуре с виртуальным терминалом **`pyte`**, встроенной поддержкой **MCP (Model Context Protocol)**, SQLite-сохранением состояния сессий, чанкингом сообщений, аудированием и интерактивным центром управления (Control Center).

---

## 🚀 Рекомендуемый ИИ-стек и MCP Экосистема

Для достижения максимальной автономности, глубинного понимания контекста и приватно-независимой работы агентов на базе **Google Antigravity (`agy`)**, настоятельно рекомендуется следующая троица **MCP-инстансов**:

```
                       ┌─────────────────────────────────────────┐
                       │        Google Antigravity (agy)        │
                       └───────────────────┬─────────────────────┘
                                           │ (Model Context Protocol)
       ┌───────────────────────────────────┼───────────────────────────────────┐
       ▼                                   ▼                                   ▼
┌───────────────┐                 ┌─────────────────┐                 ┌─────────────────┐
│ AnythingLLM   │                 │    SearXNG      │                 │    Nextcloud    │
│  (ALM RAG)    │                 │  (Web Search)   │                 │   (Work CRM)    │
└───────┬───────┘                 └────────┬────────┘                 └────────┬────────┘
        │                                  │                                   │
 Гибридный поиск:                  70+ источников,                   Файлы, контакты,
 FTS5 + BM25 + Векторы             0-telemetry, JSON                 календарь (CalDAV)
```

### 🧠 1. ALM (AnythingLLM) — Долгосрочная память и Гибридный Поиск (FTS5 + BM25)
* **Зачем агенту**: Использование чисто векторного поиска (Semantic Embeddings) часто теряет точные ключевые слова, названия функций, артикулы и имена переменных. 
* **Преимущество гибридного поиска**: Комбинация **FTS5 (Full-Text Search)** + **BM25 (Best Matching 2000)** + **Vector Proximity** гарантирует 100% точность извлечения контекста. Агент одновременно находит точные совпадения по терминам и семантически близкие фрагменты кода или документации.
* **Рекомендуемый стек & MCP**:
  - **Docker Image**: [`mintplexlabs/anythingllm`](https://github.com/Mintplex-Labs/anythingllm)
  - **MCP Server**: [`raqueljezweb/anythingllm-mcp-server`](https://github.com/raqueljezweb/anythingllm-mcp-server)

### 🔍 2. SearXNG — Чистый и Приватный Веб-Поиск
* **Зачем агенту**: Поисковые API от корпораций навязывают рекламу, лимиты и трекинг. SearXNG агрегирует результаты более чем 70 поисковиков (Google, DuckDuckGo, Wikipedia, GitHub, StackOverflow) и отдает очищенный структурированный выхлоп.
* **Преимущество**: Агент получает актуальную информацию из сети в формате JSON без зашумляющего HTML-кода и рекламных блоков.
* **Рекомендуемый стек & MCP**:
  - **Docker Image**: [`searxng/searxng`](https://github.com/searxng/searxng)
  - **MCP Server**: [`ihor-sokoliuk/mcp-searxng`](https://github.com/ihor-sokoliuk/mcp-searxng) или [`searxng-mcp`](https://pypi.org/project/searxng-mcp/)

### 💼 3. Nextcloud — Личный Work OS & Пользовательский CRM
* **Зачем агенту**: Агент должен работать с реальной жизнью пользователя — документами, договорами, задачами и встречами. Nextcloud предоставляет единую защищенную экосистему.
* **Преимущество**: Через стандартизированный MCP-интерфейс агент считывает и создает файлы, ведет календарь (CalDAV), управляет списком задач (Deck/Tasks) и хранит контакты.
* **Рекомендуемый стек & MCP**:
  - **Docker Image**: [`nextcloud/server`](https://github.com/nextcloud/server)
  - **MCP Server**: [`cbcoutinho/nextcloud-mcp-server`](https://github.com/cbcoutinho/nextcloud-mcp-server) или официальный [`Nextcloud Context Agent`](https://github.com/nextcloud/context_agent)

---

## 🌟 Главные возможности DMagyBOT
- **Model Context Protocol (MCP)**: Готовая интеграция описанной выше рекомендуемой троицы (AnythingLLM + SearXNG + Nextcloud).
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
├── mcp_config.json         # Переменные и локальные эндпоинты MCP серверов
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
# AnythingLLM (Semantic Memory & Hybrid Search)
ANYTHINGLLM_URL="http://127.0.0.1:3002"
ANYTHINGLLM_API_KEY="your_api_key"

# SearXNG (Web Search)
SEARXNG_URL="http://127.0.0.1:8889"

# Nextcloud (User CRM & Files)
NEXTCLOUD_URL="http://127.0.0.1:8000"
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
