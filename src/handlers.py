import asyncio
import logging
from aiogram import Router, types
from aiogram.filters import Command
from aiogram.types import Message, InlineKeyboardMarkup, InlineKeyboardButton, CallbackQuery
from aiogram.enums import ChatAction

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager
from src.cli_runner import AVAILABLE_MODELS, AVAILABLE_EFFORTS, AVAILABLE_MODES, get_active_account_email
from src.mcp_manager import mcp_manager
from src.audit import log_audit_event

logger = logging.getLogger(__name__)
router = Router()

def is_allowed(user_id: int) -> bool:
    return user_id in ALLOWED_USER_IDS

def get_main_menu_keyboard(session) -> InlineKeyboardMarkup:
    email = get_active_account_email()
    buttons = [
        [
            InlineKeyboardButton(text=f"🤖 {session.model_name.split('-')[0].upper()}", callback_data="menu:models"),
            InlineKeyboardButton(text=f"⚡ {session.effort.upper()}", callback_data="menu:effort")
        ],
        [
            InlineKeyboardButton(text=f"🎯 Mode: {AVAILABLE_MODES.get(session.mode, session.mode)}", callback_data="menu:mode")
        ],
        [
            InlineKeyboardButton(text=f"🔑 {email}", callback_data="menu:account"),
            InlineKeyboardButton(text="📊 Квоты (/usage)", callback_data="menu:usage")
        ],
        [
            InlineKeyboardButton(text="🔌 MCP Серверы", callback_data="menu:mcp"),
            InlineKeyboardButton(text="🔄 Сбросить сессию", callback_data="menu:reset")
        ],
        [
            InlineKeyboardButton(text="⚡ Статус системы", callback_data="menu:status")
        ]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

from src.conversations import get_available_conversations

def get_models_keyboard() -> InlineKeyboardMarkup:
    buttons = []
    for alias, full_name in AVAILABLE_MODELS.items():
        buttons.append([InlineKeyboardButton(text=f"🤖 {alias} ({full_name})", callback_data=f"set_model:{alias}")])
    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_resume_keyboard() -> InlineKeyboardMarkup:
    conversations = get_available_conversations(limit=8)
    buttons = [
        [InlineKeyboardButton(text="🔄 Продолжить последнюю сессию (--continue)", callback_data="resume_set:latest")]
    ]
    for conv in conversations:
        date_part = f" ({conv['date']})" if conv['date'] else ""
        label = f"💬 {conv['summary'][:28]}{date_part}"
        buttons.append([InlineKeyboardButton(text=label, callback_data=f"resume_set:{conv['id']}")])

    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_effort_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [
            InlineKeyboardButton(text="⚡ Low (Быстрый)", callback_data="set_effort:low"),
            InlineKeyboardButton(text="🧠 Medium (Баланс)", callback_data="set_effort:medium"),
            InlineKeyboardButton(text="🚀 High (Глубокий)", callback_data="set_effort:high")
        ],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mode_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [InlineKeyboardButton(text="💬 Standard Chat", callback_data="set_mode:default")],
        [InlineKeyboardButton(text="📋 Planning Mode (Планирование)", callback_data="set_mode:plan")],
        [InlineKeyboardButton(text="⚡ Auto-Edits Mode (Авто-правки)", callback_data="set_mode:accept-edits")],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mcp_keyboard() -> InlineKeyboardMarkup:
    servers = mcp_manager.config_manager.config.get("servers", {})
    buttons = []
    
    anythingllm_st = "✅ On" if servers.get("anythingllm", {}).get("enabled") else "⚪ Off"
    searxng_st = "✅ On" if servers.get("searxng", {}).get("enabled") else "⚪ Off"
    nextcloud_st = "✅ On" if servers.get("nextcloud", {}).get("enabled") else "⚪ Off"

    buttons.append([InlineKeyboardButton(text=f"🧠 AnythingLLM (Память): {anythingllm_st}", callback_data="toggle_mcp:anythingllm")])
    buttons.append([InlineKeyboardButton(text=f"🔍 SearXNG (Поиск): {searxng_st}", callback_data="toggle_mcp:searxng")])
    buttons.append([InlineKeyboardButton(text=f"💼 Nextcloud (CRM): {nextcloud_st}", callback_data="toggle_mcp:nextcloud")])
    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

@router.message(Command("start"), Command("help"))
async def cmd_start(message: Message):
    if not is_allowed(message.from_user.id):
        logger.warning(f"Unauthorized access attempt from user_id={message.from_user.id}")
        return

    session = session_manager.get_session(message.chat.id)
    await message.answer(
        "👋 <b>Добро пожаловать в DMagyBOT Control Center!</b>\n\n"
        "Я — высокопроизводительный асинхронный мост к <b>Google Antigravity (agy)</b> с поддержкой MCP-инфраструктуры.\n\n"
        f"🤖 <b>Модель:</b> <code>{session.model_name}</code>\n"
        f"⚡ <b>Reasoning Effort:</b> <code>{session.effort}</code>\n"
        f"🎯 <b>Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n\n"
        "<b>Доступные команды:</b>\n"
        "• /menu — Главное меню управления\n"
        "• /resume — Возобновить сохраненный диалог из истории\n"
        "• /mcp — Управление MCP серверами\n"
        "• /models — Выбор нейросетевой модели\n"
        "• /effort — Настройка глубины рассуждений\n"
        "• /mode — Режим работы (Standard/Plan)\n"
        "• /reset — Сброс сессии\n",
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("menu"))
async def cmd_menu(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        "🎛️ <b>DMagyBOT Control Center</b>\n\n"
        f"• <b>Модель:</b> <code>{session.model_name}</code>\n"
        f"• <b>Reasoning Effort:</b> <code>{session.effort}</code>\n"
        f"• <b>Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n\n"
        "Выбери параметр для настройки:",
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("mcp"))
async def cmd_mcp(message: Message):
    if not is_allowed(message.from_user.id):
        return
    report = mcp_manager.get_status_report()
    await message.answer(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")

@router.message(Command("models"))
async def cmd_models(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 <b>Текущая модель:</b> <code>{session.model_name}</code>\n\n"
        "Выбери модель для переключения:",
        reply_markup=get_models_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("effort"))
async def cmd_effort(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"⚡ <b>Текущий Reasoning Effort:</b> <code>{session.effort}</code>\n\n"
        "Выбери глубинное усилие рассуждения агента:",
        reply_markup=get_effort_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("mode"))
async def cmd_mode(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 <b>Текущий Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n\n"
        "Выбери режим исполнения:",
        reply_markup=get_mode_keyboard(),
        parse_mode="HTML"
    )

@router.callback_query(lambda c: c.data and c.data.startswith("menu:"))
async def process_menu_navigation(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    action = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)

    if action == "main":
        await callback_query.message.edit_text(
            "🎛️ <b>DMagyBOT Control Center</b>\n\n"
            f"• <b>Модель:</b> <code>{session.model_name}</code>\n"
            f"• <b>Reasoning Effort:</b> <code>{session.effort}</code>\n"
            f"• <b>Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n\n"
            "Выбери параметр для настройки:",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    elif action == "models":
        await callback_query.message.edit_text("🎯 <b>Выбор нейросетевой модели:</b>", reply_markup=get_models_keyboard(), parse_mode="HTML")
    elif action == "effort":
        await callback_query.message.edit_text("⚡ <b>Выбор глубинного уровня рассуждений (Effort):</b>", reply_markup=get_effort_keyboard(), parse_mode="HTML")
    elif action == "mode":
        await callback_query.message.edit_text("🎯 <b>Выбор режима выполнения (Execution Mode):</b>", reply_markup=get_mode_keyboard(), parse_mode="HTML")
    elif action == "mcp":
        report = mcp_manager.get_status_report()
        await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")
    elif action == "reset":
        session_manager.reset_session(callback_query.message.chat.id)
        await callback_query.message.edit_text("🔄 <b>Сессия сброшена!</b> Следующий запрос начнет новый диалог.", parse_mode="HTML")
    elif action == "account":
        email = get_active_account_email()
        kb = InlineKeyboardMarkup(inline_keyboard=[
            [InlineKeyboardButton(text="🔄 Переподключить авторизацию (Hot Reload)", callback_data="account:reload")],
            [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
        ])
        text = (
            f"🔑 <b>Авторизация Antigravity CLI</b>\n\n"
            f"👤 <b>Текущий аккаунт:</b> <code>{email}</code>\n"
            f"⚙️ <b>Подхват авторизации:</b> Автоматический (Hot Reload)\n\n"
            f"Если вы сменили аккаунт через <code>agy auth login</code> в терминале сервера, "
            f"нажмите кнопку ниже для принудительного обновления."
        )
        await callback_query.message.edit_text(text, reply_markup=kb, parse_mode="HTML")
    elif action == "usage":
        await callback_query.bot.send_chat_action(chat_id=callback_query.message.chat.id, action=ChatAction.TYPING)
        await callback_query.answer("Запрашиваю полную информацию по квотам...")
        session = session_manager.get_session(callback_query.message.chat.id)
        formatted = await session.get_usage_info()
        kb = InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]])
        try:
            await callback_query.message.edit_text(formatted, reply_markup=kb, parse_mode="HTML")
        except Exception:
            await safe_edit_text(callback_query.message, formatted)
    elif action == "status":
        email = get_active_account_email()
        await callback_query.answer(f"Status: OK | Account: {email} | Model: {session.model_name}", show_alert=True)

@router.message(Command("usage"))
async def cmd_usage(message: Message):
    if not is_allowed(message.from_user.id):
        return
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("📊 <i>Запрашиваю полную информацию по квотам моделей...</i>", parse_mode="HTML")
    session = session_manager.get_session(message.chat.id)
    formatted = await session.get_usage_info()
    await safe_edit_text(placeholder, formatted)

@router.message(Command("auth"))
@router.message(Command("account"))
async def cmd_account(message: Message):
    if not is_allowed(message.from_user.id):
        return
    email = get_active_account_email()
    kb = InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="🔄 Переподключить авторизацию (Hot Reload)", callback_data="account:reload")],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ])
    text = (
        f"🔑 <b>Авторизация Antigravity CLI</b>\n\n"
        f"👤 <b>Текущий аккаунт:</b> <code>{email}</code>\n"
        f"⚙️ <b>Подхват авторизации:</b> Автоматический (Hot Reload)\n\n"
        f"Если вы сменили аккаунт через <code>agy auth login</code> в терминале сервера, "
        f"нажмите кнопку ниже для принудительного обновления."
    )
    await message.answer(text, reply_markup=kb, parse_mode="HTML")

@router.callback_query(lambda c: c.data == "account:reload")
async def process_account_reload_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    session = session_manager.get_session(callback_query.message.chat.id)
    session.close()
    email = get_active_account_email()
    text = (
        f"⚡ <b>Авторизация успешно перезагружена!</b>\n\n"
        f"👤 <b>Активный аккаунт:</b> <code>{email}</code>\n\n"
        f"Следующий запрос пойдет с новыми учетными данными."
    )
    await callback_query.answer("Авторизация перезагружена!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("toggle_mcp:"))
async def process_mcp_toggle_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    key = callback_query.data.split(":")[1]
    new_state = mcp_manager.toggle_server(key)
    state_str = "включен ✅" if new_state else "отключен ⚪"
    await callback_query.answer(f"MCP сервер {key} {state_str}")
    report = mcp_manager.get_status_report()
    await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("set_model:"))
async def process_model_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    alias = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_model(alias):
        await callback_query.message.edit_text(
            f"✅ <b>Модель успешно изменена!</b>\nНовая модель: <code>{session.model_name}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_effort:"))
async def process_effort_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    level = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_effort(level):
        await callback_query.message.edit_text(
            f"✅ <b>Effort успешно изменен!</b>\nУровень рассуждений: <code>{session.effort.upper()}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_mode:"))
async def process_mode_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    mode_key = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_mode(mode_key):
        await callback_query.message.edit_text(
            f"✅ <b>Execution Mode успешно изменен!</b>\nРежим: <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    chat_id = message.chat.id
    if session_manager.reset_session(chat_id):
        await message.answer("🔄 <b>Сессия сброшена!</b>", parse_mode="HTML")
    else:
        await message.answer("ℹ️ Активной сессии не найдено.", parse_mode="HTML")


@router.message(Command("rename"))
async def cmd_rename(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    session = session_manager.get_session(message.chat.id)
    if not session.conversation_id:
        await message.answer("⚠️ Нет активной сессии для переименования. Сначала начните диалог или выберите через /resume.", parse_mode="HTML")
        return
        
    parts = message.text.split(maxsplit=1)
    if len(parts) < 2:
        await message.answer("ℹ️ <b>Как использовать:</b>\nОтправьте <code>/rename Новое Имя Сессии</code>", parse_mode="HTML")
        return
        
    new_title = parts[1].strip()
    
    # Needs import: from src.conversations import rename_conversation
    from src.conversations import rename_conversation
    success = rename_conversation(session.conversation_id, new_title)
    
    if success:
        await message.answer(f"✅ <b>Сессия переименована!</b>\nНовое имя: <i>{new_title}</i>", parse_mode="HTML")
    else:
        await message.answer("❌ Ошибка при переименовании. База данных недоступна или ID не найден.", parse_mode="HTML")


@router.message(Command("resume"))
async def cmd_resume(message: Message):
    if not is_allowed(message.from_user.id):
        return
    kb = get_resume_keyboard()
    await message.answer(
        "📂 <b>Выберите сохраненную сессию из истории agy CLI для возобновления:</b>",
        reply_markup=kb,
        parse_mode="HTML"
    )


@router.callback_query(lambda c: c.data and c.data.startswith("resume_set:"))
async def process_resume_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    conv_id = callback_query.data.split("resume_set:")[1]
    session = session_manager.get_session(callback_query.message.chat.id)

    if conv_id == "latest":
        session.set_conversation("latest")
        text = "🔄 <b>Возобновлена последняя активная сессия agy CLI (<code>--continue</code>)!</b>"
    else:
        session.set_conversation(conv_id)
        text = f"✅ <b>Сессия возобновлена!</b>\n\n🆔 <b>Conversation ID</b>: <code>{conv_id}</code>\n\nСледующий запрос продолжится в контексте выложенного диалога."

    await callback_query.answer("Сессия переключена!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

from aiogram.exceptions import TelegramBadRequest

async def safe_edit_text(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text."""
    try:
        await target.edit_text(text, parse_mode="HTML")
    except TelegramBadRequest as e:
        logger.warning(f"HTML parse mode failed for edit_text, falling back to plain text: {e}")
        await target.edit_text(text, parse_mode=None)

async def safe_answer(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text."""
    try:
        await target.answer(text, parse_mode="HTML")
    except TelegramBadRequest as e:
        logger.warning(f"HTML parse mode failed for answer, falling back to plain text: {e}")
        await target.answer(text, parse_mode=None)

async def send_response_chunks(message: Message, placeholder: Message, text: str, max_chunk_size: int = 3900):
    """Splits response into safe Telegram chunks (<= 4000 chars) to prevent 4096-character limit crash."""
    if len(text) <= max_chunk_size:
        await safe_edit_text(placeholder, text)
        return

    chunks = []
    current_chunk = []
    current_length = 0

    for line in text.split("\n"):
        if current_length + len(line) + 1 > max_chunk_size:
            chunks.append("\n".join(current_chunk))
            current_chunk = [line]
            current_length = len(line)
        else:
            current_chunk.append(line)
            current_length += len(line) + 1

    if current_chunk:
        chunks.append("\n".join(current_chunk))

    await safe_edit_text(placeholder, chunks[0])
    for chunk in chunks[1:]:
        if chunk.strip():
            await safe_answer(message, chunk)


@router.message()
async def handle_message(message: Message):
    if not is_allowed(message.from_user.id):
        return
    if not message.text:
        return

    # Trigger Telegram typing action
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("🤔 Думаю...")
    session = session_manager.get_session(message.chat.id)

    async def update_thinking_status(msg: Message):
        import time
        start_time = time.time()
        states = ["🤔 Думаю.", "🤔 Думаю..", "🤔 Думаю..."]
        i = 0
        while True:
            await asyncio.sleep(2.5)
            elapsed = int(time.time() - start_time)
            i = (i + 1) % len(states)
            state = states[i]
            if elapsed > 45:
                text = f"{state}\n<i>Глубокий анализ ({elapsed} сек)</i>"
            elif elapsed > 15:
                text = f"{state}\n<i>Ожидание ответа модели ({elapsed} сек)</i>"
            else:
                text = f"{state} ({elapsed} сек)"
            try:
                await msg.edit_text(text, parse_mode="HTML")
            except Exception:
                pass

    status_task = asyncio.create_task(update_thinking_status(placeholder))

    try:
        response_text = await session.get_response(message.text)
        
        # Stop the live status updating
        status_task.cancel()
        
        # Log structured execution audit log
        log_audit_event(
            user_id=message.from_user.id,
            chat_id=message.chat.id,
            model_name=session.model_name,
            effort=session.effort,
            mode=session.mode,
            prompt=message.text,
            response_length=len(response_text)
        )

        if response_text.strip():
            await send_response_chunks(message, placeholder, response_text)
        else:
            await placeholder.edit_text("⚠️ Агент отработал молча или не вернул текста.")

    except Exception as e:
        status_task.cancel()
        logger.error(f"Error handling message for chat_id={message.chat.id}: {e}", exc_info=True)
        await safe_edit_text(placeholder, f"❌ <b>Произошла ошибка:</b> {e}")
