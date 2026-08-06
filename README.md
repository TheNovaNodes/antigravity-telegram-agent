# DMagyBOT 🤖

**DMagyBOT** — это асинхронный Telegram-бот для **Google Antigravity (`agy`)**, построенный на модульной PTY-архитектуре. Он работает напрямую с CLI-бинарником через псевдо-терминалы, сохраняет контекст диалога для каждого пользователя и поддерживает динамическое переключение нейросетевых моделей в реальном времени.

---

## 🌟 Главные возможности
- **PTY-Интеграция (`pexpect`)**: Общение с `agy` без API-ключей Gemini, с использованием системной OAuth-авторизации сервера.
- **Изоляция сессий и контекста**: Каждый пользователь получает свой живой процесс `agy`, удерживающий контекст беседы.
- **Динамическая смена моделей (`/models`)**: Мгновенное переключение между Gemini, Claude и GPT-OSS при упирании в рейтлимиты.
- **Потоковый вывод (Streaming)**: Асинхронное обновление сообщений в Telegram по мере генерации токенов.
- **Буферизация старта**: Автоматическое считывание стартовых баннеров `agy` перед отправкой пользовательских промптов.
- **Безопасность**: Фильтрация доступа по Telegram User ID (`ALLOWED_USER_IDS`).
- **Автономия**: Готовый модуль `systemd` с автоперезапуском при перезагрузках сервера.

---

## 🏗️ Структура проекта
```
DMagyBOT/
├── src/
│   ├── config.py           # Валидация переменных окружения и whitelist
│   ├── cli_runner.py       # AgySession (управление PTY-процессом agy)
│   ├── session_manager.py  # SessionManager (управление сессиями чатов)
│   ├── handlers.py         # Обработчики команд Telegram и callbacks
│   └── main.py             # Точка входа приложения
├── dmagybot.service        # Unit-файл systemd
├── pyproject.toml          # Зависимости проекта
├── .env.example            # Шаблон конфигурации
└── README.md               # Документация
```

---

## 🛠️ Команды бота
- `/start` — Приветствие и справка по возможностям.
- `/models` — Интерактивное меню с кнопками для выбора активной модели.
- `/model <алиас>` — Быстрое переключение модели через команду (например `/model claude-sonnet`).
- `/reset` — Сброс текущей сессии диалога и перезапуск процесса агента.

---

## 🤖 Доступные модели
| Алиас | Полное имя модели |
| :--- | :--- |
| `gemini-flash-high` | `gemini-3.6-flash-high` |
| `gemini-flash-medium` | `gemini-3.6-flash-medium` |
| `gemini-flash-low` | `gemini-3.6-flash-low` |
| `gemini-pro-high` | `gemini-3.1-pro-high` *(По умолчанию)* |
| `gemini-pro-low` | `gemini-3.1-pro-low` |
| `claude-sonnet` | `claude-sonnet-4-6` |
| `claude-opus` | `claude-opus-4-6-thinking` |
| `gpt-oss` | `gpt-oss-120b-medium` |

---

## 🚀 Быстрый старт

### 1. Установка окружения
```bash
python3.12 -m venv .venv
source .venv/bin/activate
pip install aiogram pexpect python-dotenv
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
