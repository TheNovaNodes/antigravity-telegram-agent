import asyncio
import logging
from aiogram import Router, types
from aiogram.filters import Command
from aiogram.types import Message, InlineKeyboardMarkup, InlineKeyboardButton, CallbackQuery

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager
from src.cli_runner import AVAILABLE_MODELS, AVAILABLE_EFFORTS, AVAILABLE_MODES

logger = logging.getLogger(__name__)
router = Router()

def is_allowed(user_id: int) -> bool:
    return user_id in ALLOWED_USER_IDS

def get_main_menu_keyboard(session) -> InlineKeyboardMarkup:
    buttons = [
        [
            InlineKeyboardButton(text=f"🤖 Модель: {session.model_name.split('-')[0].upper()}", callback_data="menu:models"),
            InlineKeyboardButton(text=f"⚡ Effort: {session.effort.upper()}", callback_data="menu:effort")
        ],
        [
            InlineKeyboardButton(text=f"🎯 Mode: {AVAILABLE_MODES.get(session.mode, session.mode)}", callback_data="menu:mode")
        ],
        [
            InlineKeyboardButton(text="🔄 Сбросить сессию", callback_data="menu:reset"),
            InlineKeyboardButton(text="📊 Статус", callback_data="menu:status")
        ]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_models_keyboard() -> InlineKeyboardMarkup:
    buttons = []
    for alias, full_name in AVAILABLE_MODELS.items():
        buttons.append([InlineKeyboardButton(text=f"🤖 {alias} ({full_name})", callback_data=f"set_model:{alias}")])
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

@router.message(Command("start"))
@router.message(Command("menu"))
@router.message(Command("settings"))
async def cmd_menu(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        "🎛️ **DMagyBOT Control Center**\n\n"
        f"• **Модель:** `{session.model_name}`\n"
        f"• **Reasoning Effort:** `{session.effort}`\n"
        f"• **Execution Mode:** `{AVAILABLE_MODES.get(session.mode, session.mode)}`\n\n"
        "Выбери параметр для настройки:",
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="Markdown"
    )

@router.message(Command("models"))
async def cmd_models(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 **Текущая модель:** `{session.model_name}`\n\n"
        "Выбери модель для переключения:",
        reply_markup=get_models_keyboard(),
        parse_mode="Markdown"
    )

@router.message(Command("effort"))
async def cmd_effort(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"⚡ **Текущий Reasoning Effort:** `{session.effort}`\n\n"
        "Выбери глубинное усилие рассуждения агента:",
        reply_markup=get_effort_keyboard(),
        parse_mode="Markdown"
    )

@router.message(Command("mode"))
async def cmd_mode(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 **Текущий Execution Mode:** `{AVAILABLE_MODES.get(session.mode, session.mode)}`\n\n"
        "Выбери режим исполнения:",
        reply_markup=get_mode_keyboard(),
        parse_mode="Markdown"
    )

@router.callback_query(lambda c: c.data and c.data.startswith("menu:"))
async def process_menu_navigation(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    action = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)

    if action == "main":
        await callback_query.message.edit_text(
            "🎛️ **DMagyBOT Control Center**\n\n"
            f"• **Модель:** `{session.model_name}`\n"
            f"• **Reasoning Effort:** `{session.effort}`\n"
            f"• **Execution Mode:** `{AVAILABLE_MODES.get(session.mode, session.mode)}`\n\n"
            "Выбери параметр для настройки:",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="Markdown"
        )
    elif action == "models":
        await callback_query.message.edit_text("🎯 **Выбор нейросетевой модели:**", reply_markup=get_models_keyboard(), parse_mode="Markdown")
    elif action == "effort":
        await callback_query.message.edit_text("⚡ **Выбор глубинного уровня рассуждений (Effort):**", reply_markup=get_effort_keyboard(), parse_mode="Markdown")
    elif action == "mode":
        await callback_query.message.edit_text("🎯 **Выбор режима выполнения (Execution Mode):**", reply_markup=get_mode_keyboard(), parse_mode="Markdown")
    elif action == "reset":
        session_manager.reset_session(callback_query.message.chat.id)
        await callback_query.message.edit_text("🔄 **Сессия сброшена!** Следующий запрос начнет новый диалог.", parse_mode="Markdown")
    elif action == "status":
        await callback_query.answer(f"Status: OK | Model: {session.model_name} | Effort: {session.effort}", show_alert=True)

@router.callback_query(lambda c: c.data and c.data.startswith("set_model:"))
async def process_model_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    alias = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_model(alias):
        await callback_query.message.edit_text(
            f"✅ **Модель успешно изменена!**\nНовая модель: `{session.model_name}`",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="Markdown"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_effort:"))
async def process_effort_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    level = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_effort(level):
        await callback_query.message.edit_text(
            f"✅ **Effort успешно изменен!**\nУровень рассуждений: `{session.effort.upper()}`",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="Markdown"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_mode:"))
async def process_mode_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    mode_key = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_mode(mode_key):
        await callback_query.message.edit_text(
            f"✅ **Execution Mode успешно изменен!**\nРежим: `{AVAILABLE_MODES.get(session.mode, session.mode)}`",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="Markdown"
        )

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    chat_id = message.chat.id
    if session_manager.reset_session(chat_id):
        await message.answer("🔄 **Сессия сброшена!**")
    else:
        await message.answer("ℹ️ Активной сессии не найдено.")

from aiogram.enums import ChatAction
from src.audit import log_audit_event

async def send_response_chunks(message: Message, placeholder: Message, text: str, max_chunk_size: int = 3900):
    """Splits response into safe Telegram chunks (<= 4000 chars) to prevent 4096-character limit crash."""
    if len(text) <= max_chunk_size:
        await placeholder.edit_text(text)
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

    await placeholder.edit_text(chunks[0])
    for chunk in chunks[1:]:
        if chunk.strip():
            await message.answer(chunk)


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

    try:
        response_text = await session.get_response(message.text)
        
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
        logger.error(f"Error handling message for chat_id={message.chat.id}: {e}", exc_info=True)
        await placeholder.edit_text(f"❌ **Произошла ошибка:** {e}")
