import asyncio
import logging
from aiogram import Router, types
from aiogram.filters import Command
from aiogram.types import Message

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager

logger = logging.getLogger(__name__)
router = Router()

def is_allowed(user_id: int) -> bool:
    return user_id in ALLOWED_USER_IDS

@router.message(Command("start"))
async def cmd_start(message: Message):
    if not is_allowed(message.from_user.id):
        return
    await message.answer(
        "👋 **Привет! Я DMagyBOT** (Interactive PTY Wrapper).\n\n"
        "Я работаю напрямую с CLI `agy` в изолированном PTY-контексте, сохраняя контекст разговора!\n\n"
        "Команды:\n"
        "/reset — Сбросить текущую сессию и начать заново."
    )

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    chat_id = message.chat.id
    if session_manager.reset_session(chat_id):
        await message.answer("🔄 **Сессия сброшена!** Следующее сообщение начнет новый диалог.")
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
