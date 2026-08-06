import asyncio
import logging
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
