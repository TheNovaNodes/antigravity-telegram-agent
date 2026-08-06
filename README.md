# DMagyBOT 🤖

**DMagyBOT** — это высокопроизводительный асинхронный Telegram-бот для **Google Antigravity (`agy`)**, построенный на PTY-архитектуре с виртуальным терминалом **`pyte`**, встроенным SQLite-сохранением состояния сессий, чанкингом сообщений, аудированием и интерактивным центром управления (Control Center).

---

## 🌟 Главные возможности
- **PTY-Интеграция (`pexpect` + `pyte`)**: Работа с CLI-агентом `agy` без прямого API-ключа Gemini, используя системную OAuth-авторизацию сервера с чистым выводом ответа.
- **Интерактивный Control Center (`/menu`)**: Наглядная панель с Inline-кнопками в Telegram для мгновенного выбора моделей, уровней усилий (`effort`) и режимов выполнения (`normal`, `yolo`, `safe`).
- **Персистентность сессий (SQLite)**: База данных `data/bot.db` сохраняет состояние `AgySession` (модель, режим, effort) и автоматически восстанавливает его при перезапусках и деплое бота.
- **Чанкинг ответов**: Автоматическое разделение ответов превышающих лимит Telegram в 4096 символов без потери форматирования.
- **Аудит действий (`logs/audit.log`)**: Безопасный логгер в формате JSON, фиксирующий запросы пользователей, Telegram ID, выбранные модели и время выполнения.
- **Автоматическая очистка ресурсов**: Фоновый процесс удаляет неактивные сессии (Idle > 30 мин) каждые 5 минут, предотвращая утечки PTY-процессов и памяти.
- **Полное тестовое покрытие**: Автоматический пакет unittest (20/20 пройденных тестов) и подготовленный GitHub Actions CI workflow.
- **Безопасность**: Валидация конфигурации при старте и фильтрация доступа по Telegram User ID (`ALLOWED_USER_IDS`).

---

## 🏗️ Структура проекта
```
DMagyBOT/
├── .github/
│   └── workflows/
│       └── ci.yml          # GitHub Actions CI workflow
├── src/
│   ├── config.py           # Валидация переменных окружения и whitelist
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
│   └── test_session_manager.py
├── data/                   # Хранилище SQLite базы данных (bot.db)
├── logs/                   # Журналы аудита (audit.log)
├── dmagybot.service        # Unit-файл systemd
├── pyproject.toml          # Зависимости проекта
├── .env.example            # Шаблон конфигурации
└── README.md               # Документация
```

---

## 🛠️ Команды бота
- `/start` — Приветствие и краткое руководство.
- `/menu` — Открыть интерактивный Control Center Dashboard (Модель, Effort, Режим).
- `/status` — Просмотр текущей конфигурации сессии и состояния системы.
- `/models` — Селектор модели через Inline-клавиатуру.
- `/effort` — Настройка уровня генерации и глубины рассуждений (`low`, `medium`, `high`).
- `/mode` — Выбор режима выполнения (`normal`, `yolo`, `safe`).
- `/reset` / `/clear` — Сброс текущей сессии диалога и очистка контекста.
- `/help` — Подробная справка по всем функциям бота.

---

## 🤖 Доступные модели
| Алиас | Полное имя модели |
| :--- | :--- |
| `gemini-flash` | `gemini-3.6-flash` |
| `gemini-pro` | `gemini-3.1-pro` |
| `claude-sonnet` | `claude-3-5-sonnet` |
| `gpt-4o` | `gpt-4o` |

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
Содержимое `.env`:
```env
TELEGRAM_BOT_TOKEN="ваш_токен_от_botfather"
ALLOWED_USER_IDS="173681771"
AGY_BINARY_PATH="/root/.local/bin/agy"
LOG_LEVEL="INFO"
```

### 3. Деплой через systemd
```bash
cp dmagybot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now dmagybot
```
