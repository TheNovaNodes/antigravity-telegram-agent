# DMagyBOT 🤖

**DMagyBOT** — это нелокальный Telegram-мост для **Google Antigravity (`agy`)**, работающий напрямую через псевдо-терминал (PTY) и поддерживающий многопользовательские асинхронные сессии в реальном времени.

## 🌟 Особенности
- **Интерактивный PTY-контекст**: Работает с бинарником `agy` напрямую без необходимости дополнительных API-ключей.
- **Изоляция сессий**: Хранит контекст диалога для каждого `chat_id` отдельно.
- **Стриминг ответов**: Вывод информации в Telegram в режиме реального времени.
- **Безопасность**: Жесткая фильтрация доступа по Telegram User ID (`ALLOWED_USER_IDS`).
- **Автономия**: Запуск под управлением `systemd` с автоперезапуском при сбоях.

## 🚀 Быстрый старт

### 1. Установка зависимостей
```bash
python3.12 -m venv .venv
source .venv/bin/activate
pip install -r pyproject.toml # или pip install aiogram pexpect python-dotenv
```

### 2. Конфигурация `.env`
Создайте `.env` файл на основе `.env.example`:
```env
TELEGRAM_BOT_TOKEN="ваш_токен_от_botfather"
ALLOWED_USER_IDS="173681771"
AGY_BINARY_PATH="/root/.local/bin/agy"
LOG_LEVEL="INFO"
```

### 3. Запуск через systemd
```bash
cp dmagybot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now dmagybot
```

## 🛠️ Команды бота
- `/start` — Приветствие и справка.
- `/reset` — Сброс текущей сессии агента для чата.
