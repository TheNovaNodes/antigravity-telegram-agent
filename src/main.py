import asyncio
import logging
import os
import sys

# Ensure project root is in sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiogram import Bot, Dispatcher
from src.config import BOT_TOKEN, LOG_LEVEL
from src.handlers import router

logging.basicConfig(level=getattr(logging, LOG_LEVEL))
logger = logging.getLogger(__name__)

async def main():
    bot = Bot(token=BOT_TOKEN)
    dp = Dispatcher()
    dp.include_router(router)

    logger.info("🚀 Starting DMagyBOT (Modular PTY Architecture)...")
    await dp.start_polling(bot)

if __name__ == "__main__":
    asyncio.run(main())
