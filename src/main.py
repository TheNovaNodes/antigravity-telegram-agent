import asyncio
import logging
import os
import sys

# Ensure project root is in sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiogram import Bot, Dispatcher
from aiogram.types import BotCommand

from src.config import BOT_TOKEN, LOG_LEVEL
from src.db import init_db
from src.handlers import router

logging.basicConfig(level=getattr(logging, LOG_LEVEL))
logger = logging.getLogger(__name__)

async def main():
    # Initialize SQLite DB for persistent session state across deployments
    init_db()

    bot = Bot(token=BOT_TOKEN)
    dp = Dispatcher()
    dp.include_router(router)

    # Register Telegram native slash command menu
    await bot.set_my_commands([
        BotCommand(command="menu", description="🎛️ Главный центр управления DMagyBOT"),
        BotCommand(command="models", description="🤖 Выбор нейросети (Gemini, Claude, GPT)"),
        BotCommand(command="effort", description="⚡ Глубина рассуждений (low/medium/high)"),
        BotCommand(command="mode", description="🎯 Режим работы (Plan / Auto-Edits / Standard)"),
        BotCommand(command="reset", description="🔄 Сбросить сессию агента"),
        BotCommand(command="start", description="👋 Справка и старт"),
    ])

    logger.info("🚀 Starting DMagyBOT (Control Center & Pyte PTY Architecture)...")
    await dp.start_polling(bot)

if __name__ == "__main__":
    asyncio.run(main())
