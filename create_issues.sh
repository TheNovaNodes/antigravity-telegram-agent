#!/bin/bash
TOKEN="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"
REPO="https://api.github.com/repos/TheNovaNodes/antigravity-go-tg-bot-agent/issues"

post_issue() {
  local title="$1"
  local body="$2"
  # properly escape JSON payload using jq
  jq -n --arg title "$title" --arg body "$body" '{title: $title, body: $body}' | \
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Accept: application/vnd.github.v3+json" -d @- "$REPO" > /dev/null
}

BODY1="### Описание
Текущая архитектура использует глобальную мапу \`globalSessions[botName]\` для хранения сессий. 
Это приводит к тому, что все пользователи бота делят один инстанс.
- Если у пользователей разные настройки (модель/воркспейс), они циклично убивают (Kill) процессы друг друга.
- Если настройки одинаковые, они шлют команды в один и тот же stdin агента.

### Как исправить
Перейти на использование \`map[int64]*AgySession\` с ключом по \`userID\` или \`chatID\`, чтобы обеспечить изоляцию сессий."

post_issue "Architectural Flaw: Singleton Session State Tied to Bot Name" "$BODY1"

BODY2="### Описание
В коде жёстко захардкожены абсолютные пути вида \`/root/.agents/%s\`.
Это привязывает выполнение приложения к пользователю \`root\` и убивает любую портативность приложения (не запустится ни локально у других разработчиков, ни в непривилегированном Docker-контейнере).

### Как исправить
Использовать \`os.UserHomeDir()\` или передавать путь через переменные окружения."

post_issue "Security/Portability: Hardcoded Absolute Paths to /root" "$BODY2"

BODY3="### Описание
Множественные вызовы \`db.Exec()\` (например, в функциях \`updateUserSession\`, \`updateUserModel\`) не проверяют возвращаемую ошибку.
В случае лока базы данных (SQLite lock) или сбоя диска приложение молча проглатывает ошибку. UI отображает успешное действие, но данные теряются.

### Как исправить
Обязательно обрабатывать \`err\` после \`db.Exec()\`, логировать сбои и, при необходимости, возвращать ошибку пользователю, а не имитировать успех."

post_issue "Data Integrity Risk: Unhandled Database Execution Errors" "$BODY3"

BODY4="### Описание
В функции \`SplitHTMLChunks\` используется срез строки по байтам: \`part := p[:maxChunkSize]\`.
Поскольку в Go строки представляют собой слайсы байт UTF-8, такое отсечение может разделить многобайтовый символ (например, кириллицу или эмодзи) пополам.
Это приводит к формированию невалидного UTF-8. При попытке отправить такой чанк Telegram API возвращает ошибку \`400 Bad Request\`.

### Как исправить
Сначала конвертировать строку в слайс рун: \`runes := []rune(p)\`, а затем делать срез по рунам."

post_issue "Bug: Byte-level String Slicing Corrupts Unicode Characters" "$BODY4"
