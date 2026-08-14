import os

file_path = 'src/handlers.py'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

replacements = {
    '◀️ Назад в меню': '◀️ Back to Menu',
    '🆕 Новая сессия (чистый диалог)': '🆕 New Session (Clean Chat)',
    '🔄 Продолжить последнюю сессию (--continue)': '🔄 Resume Latest Session (--continue)',
    '⚡ Low (Быстрый)': '⚡ Low (Fast)',
    '🧠 Medium (Баланс)': '🧠 Medium (Balanced)',
    '🚀 High (Глубокий)': '🚀 High (Deep)',
    '📋 Planning Mode (Планирование)': '📋 Planning Mode',
    '⚡ Auto-Edits Mode (Авто-правки)': '⚡ Auto-Edits Mode',
    '🧠 AnythingLLM (Память)': '🧠 AnythingLLM (Memory)',
    '🔍 SearXNG (Поиск)': '🔍 SearXNG (Search)',
    '🎯 <b>Текущая модель:</b>': '🎯 <b>Current Model:</b>',
    'Выбери модель для переключения:': 'Select a model to switch:',
    '⚡ <b>Текущий Reasoning Effort:</b>': '⚡ <b>Current Reasoning Effort:</b>',
    'Выбери глубинное усилие рассуждения агента:': "Select the agent's reasoning effort depth:",
    '🎯 <b>Текущий Execution Mode:</b>': '🎯 <b>Current Execution Mode:</b>',
    'Выбери режим исполнения:': 'Select execution mode:',
    '🎯 <b>Выбор нейросетевой модели:</b>': '🎯 <b>AI Model Selection:</b>',
    '⚡ <b>Выбор глубинного уровня рассуждений (Effort):</b>': '⚡ <b>Reasoning Effort Selection:</b>',
    '🎯 <b>Выбор режима выполнения (Execution Mode):</b>': '🎯 <b>Execution Mode Selection:</b>',
    '📂 <b>Текущий Workspace:</b>': '📂 <b>Current Workspace:</b>',
    'Выбери проект/папку для закрепления на всю сессию:': 'Select a project/folder to pin for the session:',
    '⚡ <b>Авторизация успешно перезагружена!</b>': '⚡ <b>Authorization successfully reloaded!</b>',
    '👤 <b>Активный аккаунт:</b>': '👤 <b>Active Account:</b>',
    'Следующий запрос пойдет с новыми учетными данными.': 'The next request will use the new credentials.',
    'Авторизация перезагружена!': 'Authorization reloaded!',
    'включен ✅': 'enabled ✅',
    'отключен ⚪': 'disabled ⚪',
    'MCP сервер ': 'MCP server ',
    'Модель изменена!': 'Model changed!',
    '✅ <b>Модель успешно изменена!</b>\\nНовая модель:': '✅ <b>Model successfully changed!</b>\\nNew model:',
    'Эта модель уже выбрана!': 'This model is already selected!',
    'Effort изменен!': 'Effort changed!',
    '✅ <b>Effort успешно изменен!</b>\\nУровень рассуждений:': '✅ <b>Effort successfully changed!</b>\\nReasoning level:',
    'Этот effort уже выбран!': 'This effort is already selected!',
    'Режим изменен!': 'Mode changed!',
    '✅ <b>Execution Mode успешно изменен!</b>\\nРежим:': '✅ <b>Execution Mode successfully changed!</b>\\nMode:',
    'Этот режим уже выбран!': 'This mode is already selected!',
    '✨ <b>Новая сессия создана!</b>': '✨ <b>New session created!</b>',
    'Настройки сохранены:': 'Settings saved:',
    'Следующий запрос начнёт чистый диалог.': 'The next request will start a clean chat.',
    '⚠️ Нет активной сессии для переименования. Сначала начните диалог или выберите через /resume.': '⚠️ No active session to rename. First start a chat or select one via /resume.',
    'ℹ️ <b>Как использовать:</b>\\nОтправьте <code>/rename Новое Имя Сессии</code>': 'ℹ️ <b>How to use:</b>\\nSend <code>/rename New Session Name</code>',
    '✅ <b>Сессия переименована!</b>\\nНовое имя:': '✅ <b>Session renamed!</b>\\nNew name:',
    '❌ Ошибка при переименовании. База данных недоступна или ID не найден.': '❌ Error renaming. Database unavailable or ID not found.',
    'ℹ️ <b>Как использовать:</b>\\nОтправьте <code>/track_jules ИмяСессииJules</code>': 'ℹ️ <b>How to use:</b>\\nSend <code>/track_jules JulesSessionName</code>',
    '✅ <b>Jules сессия добавлена в мониторинг!</b>\\nИмя:': '✅ <b>Jules session added to monitoring!</b>\\nName:',
    '\\n\\nВы получите уведомление, когда она завершится.': '\\n\\nYou will receive a notification when it finishes.',
    '🏠 Домашняя директория (/root)': '🏠 Home directory (/root)',
    '❌ Ошибка: Папка не найдена': '❌ Error: Folder not found',
    '\\n\\n💬 <b>Текущая:</b> Последняя активная (--continue)': '\\n\\n💬 <b>Current:</b> Latest active (--continue)',
    '\\n\\n💬 <b>Текущая:</b>': '\\n\\n💬 <b>Current:</b>',
    '📂 <b>Выберите сохраненную сессию из истории agy CLI для возобновления:</b>': '📂 <b>Select a saved session from agy CLI history to resume:</b>',
    '✨ <b>Новая чистая сессия создана!</b>\\nНастройки сохранены. Следующий запрос начнёт новый диалог.': '✨ <b>New clean session created!</b>\\nSettings saved. The next request will start a new chat.',
    '🔄 <b>Возобновлена последняя активная сессия agy CLI (<code>--continue</code>)!</b>': '🔄 <b>Resumed latest active agy CLI session (<code>--continue</code>)!</b>',
    '\\n📝 <b>Название:</b> <i>{title}</i>': '\\n📝 <b>Name:</b> <i>{title}</i>',
    '✅ <b>Сессия возобновлена!</b>\\n\\n🆔 <b>Conversation ID</b>: <code>{conv_id}</code>{title_display}\\n\\nСледующий запрос продолжится в контексте выбранного диалога.': '✅ <b>Session resumed!</b>\\n\\n🆔 <b>Conversation ID</b>: <code>{conv_id}</code>{title_display}\\n\\nThe next request will continue in the context of the selected chat.',
    'Сессия переключена!': 'Session switched!',
    '📄 <i>[Ответ слишком большой. Полная версия в файле ниже]</i>': '📄 <i>[Response too large. Full version in the file below]</i>',
    '📄 <b>Полный ответ AntigravityTelegramAgent</b>': '📄 <b>Full AntigravityTelegramAgent response</b>',
    '🤔 Думаю...': '🤔 Thinking...',
    '\\n\\n<i>⏳ Печатаю...</i>': '\\n\\n<i>⏳ Typing...</i>',
    '⚠️ <b>Агент отработал молча или не вернул текст.</b>': '⚠️ <b>Agent executed silently or returned no text.</b>',
    '📌 <i>Возможные причины:</i>': '📌 <i>Possible reasons:</i>',
    'или <code>high</code> effort скрыла фазу мышления или превысила таймаут PTY-экрана.': 'or <code>high</code> effort hid the reasoning phase or exceeded the PTY screen timeout.',
    'На серверах модели возникла кратковременная пауза': 'There was a temporary pause on the model servers',
    '💡 <b>Решение:</b>': '💡 <b>Solution:</b>',
    'Повторите запрос или используйте': 'Repeat the request or use',
    'для выбора другой модели.': 'to select a different model.',
    'Или снизите': 'Or lower',
    'до': 'to',
    '❌ <b>Произошла ошибка:</b>': '❌ <b>An error occurred:</b>'
}

for k, v in replacements.items():
    content = content.replace(k, v)

# special fixes for things that were broken into parts
content = content.replace('• Модель', '• The model')
content = content.replace('Повторите запрос или используйте <code>/models</code> to select a different model.', 'Repeat the request or use <code>/models</code> to select a different model.')
content = content.replace('Or lower <code>/effort</code> to <code>medium</code>.', 'Or lower <code>/effort</code> to <code>medium</code>.')
content = content.replace('There was a temporary pause on the model servers (Capacity/Thinking suppression).', 'There was a temporary pause on the model servers (Capacity/Thinking suppression).')
content = content.replace('• The model <code>{session.model_name}</code> or <code>high</code> effort hid the reasoning phase or exceeded the PTY screen timeout.', '• The model <code>{session.model_name}</code> or <code>high</code> effort hid the reasoning phase or exceeded the PTY screen timeout.')

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)
