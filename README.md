# DMagyBOT 🤖

**DMagyBOT** — промышленный асинхронный Telegram-бот для **Google Antigravity (`agy`)**, построенный на базе виртуального терминала **`pyte`** в PTY-архитектуре, с интеграцией протокола **Model Context Protocol (MCP)**, персистентным хранением состояний сессий в SQLite, многоуровневым чанкингом ответов, аудированием операций и интерактивным центром управления (Control Center).

---

## 🚀 Рекомендуемый ИИ-стек и MCP Архитектура

Для обеспечения автономности, точного контекстного поиска и независимости от вендоров рекомендуется архитектурная триада MCP-сервисов:

```
                       ┌─────────────────────────────────────────┐
                       │        Google Antigravity (agy)        │
                       └───────────────────┬─────────────────────┘
                                           │ (Model Context Protocol)
       ┌───────────────────────────────────┼───────────────────────────────────┐
       ▼                                   ▼                                   ▼
┌─────────────────────────┐       ┌─────────────────────────┐         ┌─────────────────┐
│  nova-anythingllm-mcp   │       │    nova-searxng-mcp    │         │    Nextcloud    │
│  (TheNovaNodes RAG)     │       │  (TheNovaNodes Search)  │         │   (Work CRM)    │
└────────────┬────────────┘       └────────────┬────────────┘         └────────┬────────┘
             │                                 │                               │
  Гибридный поиск:                Метапоиск (90+ движков),           Файлы, контакты,
  FTS5 + BM25 + Vectors           Deep Research & Markdown           календарь (CalDAV)
```

---

## 🛡️ Двухуровневое разделение MCP: Control Plane & Data Plane

Для обеспечения информационной безопасности и оптимизации расхода контекстного окна (Context Budget Efficiency) используется двухуровневая классификация MCP-инструментов:

```
                          ┌───────────────────────────┐
                          │   Двухуровневая MCP       │
                          │      Архитектура          │
                          └─────────────┬─────────────┘
                                        │
           ┌────────────────────────────┴────────────────────────────┐
           ▼                                                         ▼
┌──────────────────────────────┐                          ┌──────────────────────────────┐
│  1. Control Plane MCP        │                          │  2. Data Plane MCP           │
│  (Управление инфраструктурой) │                          │  (Операционное исполнение)   │
├──────────────────────────────┤                          ├──────────────────────────────┤
│ • Администрирование воркспейсов│                          │ • Поиск по векторно-лексической│
│ • Управление API-ключами      │                          │   базе знании (FTS5 + BM25)   │
│ • Конфигурация движков поиска │                          │ • Агрегированный метапоиск   │
│ • Изоляция прав доступа      │                          │ • Чтение/Запись CRM файлов   │
└──────────────────────────────┘                          └──────────────────────────────┘
```

1. **Control Plane MCP (Управление)**: Содержит административные функции. Отделен для предотвращения уязвимостей типа `Prompt Injection` и исключения лишних административных схем из пользовательского диалога.
2. **Data Plane MCP (Исполнение)**: Передает агенту только легковесные операционные инструменты (поиск по семантической памяти, веб-поиск, работа с файлами пользователя).

---

## 🧠 Высокопроизводительные MCP Гейтвеи от TheNovaNodes

Для работы с семантической памятью и веб-поиском рекомендуются специализированные MCP-репозитории организации **[TheNovaNodes](https://github.com/TheNovaNodes)**, обладающие уникальными архитектурными и инженерными преимуществами:

### 1. 🧠 Семантическая память: [`TheNovaNodes/nova-anythingllm-mcp`](https://github.com/TheNovaNodes/nova-anythingllm-mcp)
* **Инженерные особенности**:
  - **Гибридный поиск нового поколения**: Объединяет лексический поиск **FTS5 + BM25** с векторным сходством через взвешенную слияющую оценку (RRF — Reciprocal Rank Fusion / Weighted Merge, калиброванную по метрикам NDCG). Это решает проблему потери точных терминов, наименований функций и артикулов, характерную для чисто векторных решений.
  - **Context Assembly**: Автоматически достраивает найденные совпадения до полного абзацного контекста.
  - **Gatekeeping & Diagnostics**: Встроенные механизмы проверки здоровья (`gateway_health`) и ограничение параллелизма (Fan-out Throttle) для защиты инстанса AnythingLLM.
* **Репозиторий**: [`TheNovaNodes/nova-anythingllm-mcp`](https://github.com/TheNovaNodes/nova-anythingllm-mcp) (Пакет PyPI: `nova-memory-gateway`).

### 🔍 2. Глубокий веб-поиск: [`TheNovaNodes/nova-searxng-mcp`](https://github.com/TheNovaNodes/nova-searxng-mcp)
* **Инженерные особенности**:
  - **Метапоиск по 90+ источникам**: Агрегирует выдачу без рекламы и пользовательского трекинга.
  - **Deep Research Orchestration**: Поддерживает многошаговый оркестрируемый поиск с синтезом источников и автоматической очисткой JS-страниц в формат Markdown.
  - **Слияние с семантической памятью**: Возможность мгновенной интеграции результатов поиска с локальной базой знаний.
* **Репозиторий**: [`TheNovaNodes/nova-searxng-mcp`](https://github.com/TheNovaNodes/nova-searxng-mcp).

### 💼 3. Work OS & CRM: [`cbcoutinho/nextcloud-mcp-server`](https://github.com/cbcoutinho/nextcloud-mcp-server)
* **Инженерные особенности**:
  - Обеспечивает интеграцию с персональным облаком Nextcloud (файловая система, календарь CalDAV, задачи Deck и контакты).
* **Репозиторий**: [`cbcoutinho/nextcloud-mcp-server`](https://github.com/cbcoutinho/nextcloud-mcp-server) (или официальный [`nextcloud/context_agent`](https://github.com/nextcloud/context_agent)).

---

## 🌟 Возможности DMagyBOT
- **Model Context Protocol (MCP)**: Полная совместимость с экосистемой `TheNovaNodes` (Control Plane / Data Plane).
- **PTY-Архитектура (`pexpect` + `pyte`)**: Эмуляция терминала для прямого взаимодействия с `agy` без дополнительных API-ключей Gemini.
- **Интерактивный панель управления (`/menu`, `/mcp`)**: Модульный интерфейс Telegram для настройки моделей, глубин рассуждений (`effort`), режимов работы и состояния MCP-серверов.
- **Персистентность сессий (SQLite)**: База данных `data/dmagybot.db` сохраняет конфигурацию пользователей и обеспечивает возобновление сессий после перезапуска.
- **Автоматический Hot Reload авторизации**: Мониторинг сигнатуры файлов `~/.gemini/antigravity-cli/antigravity-oauth-token` и `settings.json`. При смене аккаунта через `agy auth login` на сервере бот автоматически подхватывает новые креды без ручного рестарта.
- **Форматирование Dyslexia-Friendly & Очистка TUI**: Автоматическая склейка рваных строк терминала в плавные естественные абзацы с двойным отступом (`\n\n`), полным удалением ASCII-арта (`▄▀▀`), эхо команд (`> ...`) и служебных рамок.
- **Перехват системных ошибок**: Автоматический перехват `Eligibility Check` и `Quota Exceeded` с выдачей понятных пошаговых рекомендаций на русском языке.
- **Безопасный чанкинг**: Алгоритм разделения ответов, гарантирующий соблюдение ограничений Telegram API (4096 символов).
- **Аудит операций (`logs/audit.log`)**: Журналирование событий в формате JSON для контроля исполняемых команд и моделей.
- **Автоматическая очистка ресурсов**: Фоновый процесс удаления неактивных сессий (Idle > 30 мин).

---

## 🏗️ Структура проекта
```
DMagyBOT/
├── .github/
│   └── workflows/
│       └── ci.yml          # GitHub Actions CI workflow
├── src/
│   ├── config.py           # Валидация конфигурации окружения
│   ├── mcp_config.py       # Менеджер MCP-серверов (TheNovaNodes & Custom Gateways)
│   ├── mcp_manager.py      # Модуль проверки статуса и управления MCP
│   ├── cli_runner.py       # AgySession (PTY-процессы agy, pyte и Hot Reload авторизации)
│   ├── formatters.py       # Dyslexia-Friendly форматирование и перехват системных ошибок
│   ├── session_manager.py  # SessionManager (Управление жизненным циклом сессий и Idle TTL)
│   ├── db.py               # Персистентность сессий в SQLite (data/dmagybot.db)
│   ├── audit.py            # Журналирование аудита в JSON (logs/audit.log)
│   ├── handlers.py         # Обработчики команд Telegram и callbacks
│   └── main.py             # Точка входа приложения и инициализация сервисов
├── tests/                  # Набор из 29 автоматизированных unittest-тестов
│   ├── test_audit.py
│   ├── test_auth_hot_reload.py
│   ├── test_chunking.py
│   ├── test_cli_runner.py
│   ├── test_config.py
│   ├── test_db_persistence.py
│   ├── test_formatters.py
│   ├── test_handlers.py
│   ├── test_mcp.py
│   └── test_session_manager.py
├── data/                   # База данных SQLite (dmagybot.db)
├── logs/                   # Журналы аудита (audit.log)
├── mcp_config.json         # Локальные эндпоинты MCP-серверов
├── dmagybot.service        # Юнит-файл systemd
├── pyproject.toml          # Зависимости проекта
├── .env.example            # Шаблон конфигурации
└── README.md               # Документация
```

---

## 🛠️ Команды бота
- `/start` — Инициализация бота и краткая справка.
- `/menu` — Интерактивный Центр Управления (Control Center).
- `/mcp` — Панель управления MCP-серверами.
- `/status` — Мониторинг состояния сессии и параметров системы.
- `/models` — Переключение нейросетевых моделей (Gemini, Claude, GPT).
- `/effort` — Настройка глубины рассуждений (`low`, `medium`, `high`).
- `/mode` — Выбор режима работы (`Standard`, `Plan`, `Auto-Edits`).
- `/reset` / `/clear` — Сброс активного контекста сессии.
- `/help` — Справочное руководство.

---

## 🔌 Конфигурация MCP-серверов

Параметры MCP-серверов настраиваются через `mcp_config.json` или переменные окружения `.env`:

```env
# TheNovaNodes AnythingLLM Gateway
ANYTHINGLLM_URL="http://127.0.0.1:3002"
ANYTHINGLLM_API_KEY="your_api_key"

# TheNovaNodes SearXNG Gateway
SEARXNG_URL="http://127.0.0.1:8889"

# Nextcloud CRM Gateway
NEXTCLOUD_URL="http://127.0.0.1:8000"
NEXTCLOUD_USER="username"
NEXTCLOUD_PASS="app_password"
```

---

## 🧪 Тестирование

Запуск полного пакета автоматизированных тестов:
```bash
.venv/bin/python -m unittest discover -s tests
```

---

## 🚀 Развертывание

### 1. Подготовка окружения
```bash
python3.12 -m venv .venv
source .venv/bin/activate
pip install aiogram pexpect pyte python-dotenv
```

### 2. Конфигурация
```bash
cp .env.example .env
chmod 600 .env
```

### 3. Запуск через systemd
```bash
cp dmagybot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now dmagybot
```
