import asyncio
import logging
from aiogram import Router, types
from aiogram.filters import Command
from aiogram.types import Message, InlineKeyboardMarkup, InlineKeyboardButton, CallbackQuery

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager
from src.cli_runner import AVAILABLE_MODELS

logger = logging.getLogger(__name__)
router = Router()

def is_allowed(user_id: int) -> bool:
    return user_id in ALLOWED_USER_IDS

def get_models_keyboard() -> InlineKeyboardMarkup:
    buttons = []
    for alias, full_name in AVAILABLE_MODELS.items():
        buttons.append([InlineKeyboardButton(text=f"🤖 {alias} ({full_name})", callback_data=f"set_model:{alias}")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

@router.message(Command("start"))
async def cmd_start(message: Message):
    if not is_allowed(message.from_user.id):
        return
    await message.answer(
        "👋 **Привет! Я DMagyBOT** (Interactive PTY Wrapper).\n\n"
        "Команды:\n"
        "• `/models` — Показать и переключить активную нейросеть.\n"
        "• `/reset` — Сбросить текущую сессию диалога.\n"
        "• `/model <имя>` — Быстрое переключение модели."
    )

@router.message(Command("models"))
async def cmd_models(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 **Текущая модель:** `{session.model_name}`\n\n"
        "Выбери модель для переключения при рейтлимитах:",
        reply_markup=get_models_keyboard(),
        parse_mode="Markdown"
    )

@router.callback_query(lambda c: c.data and c.data.startswith("set_model:"))
async def process_model_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    alias = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_model(alias):
        await callback_query.message.edit_text(
            f"✅ **Модель успешно изменена!**\nНовая модель: `{session.model_name}`",
            parse_mode="Markdown"
        )
    else:
        await callback_query.answer("❌ Ошибка при выборе модели", show_alert=True)

@router.message(Command("model"))
async def cmd_model(message: Message):
    if not is_allowed(message.from_user.id):
        return
    args = message.text.split(maxsplit=1)
    if len(args) < 2:
        await message.answer("Использование: `/model <gemini-flash-high|claude-sonnet|gpt-oss|...>`")
        return
    model_alias = args[1].strip()
    session = session_manager.get_session(message.chat.id)
    if session.set_model(model_alias):
        await message.answer(f"✅ **Модель изменена на:** `{session.model_name}`", parse_mode="Markdown")
    else:
        await message.answer(f"❌ Неизвестная модель `{model_alias}`. Используй `/models` для списка.", parse_mode="Markdown")

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    chat_id = message.chat.id
    if session_manager.reset_session(chat_id):
        await message.answer("🔄 **Сессия сброшена!**")
    else:
        await message.answer("ℹ️ Активной сессии не найдено.")

@router.message()
async def handle_message(message: Message):
    if not is_allowed(message.from_user.id):
        return
    if not message.text:
        return

    placeholder = await message.answer("🤔 Запускаю агента...")
    session = session_manager.get_session(message.chat.id)

    full_response = ""
    last_update_time = asyncio.get_event_loop().time()
    update_interval = 1.5

    try:
        async for chunk in session.stream_chat(message.text):
            full_response += chunk
            current_time = asyncio.get_event_loop().time()
            if current_time - last_update_time > update_interval:
                if full_response.strip():
                    try:
                        await placeholder.edit_text(full_response + " ⏳")
                        last_update_time = current_time
                    except Exception:
                        pass

        final_text = full_response.strip()
        if final_text:
            await placeholder.edit_text(final_text)
        else:
            await placeholder.edit_text("⚠️ Агент отработал молча или не вернул текста.")

    except Exception as e:
        logger.error(f"Error handling message for chat_id={message.chat.id}: {e}", exc_info=True)
        await placeholder.edit_text(f"❌ **Произошла ошибка:** {e}")
