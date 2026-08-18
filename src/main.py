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
from src.session_manager import session_manager
from src.jules_monitor import monitor_jules_sessions
from src.scheduler import sentinel_scheduler

logging.basicConfig(level=getattr(logging, LOG_LEVEL))
logger = logging.getLogger(__name__)

async def main():
    # Initialize SQLite DB for persistent session state across deployments
    init_db()

    # Start background idle session cleanup loop (every 5 mins, 30 min TTL)
    asyncio.create_task(session_manager.start_cleanup_loop())

    bot = Bot(token=BOT_TOKEN)
    
    # Initialize Autonomous Sentinel Scheduler with Bot instance
    sentinel_scheduler.set_bot(bot)
    sentinel_scheduler.start()
    
    # Start Jules sessions monitor loop
    asyncio.create_task(monitor_jules_sessions(bot))

    dp = Dispatcher()
    dp.include_router(router)

    # Register Telegram native slash command menu
    await bot.set_my_commands([
        BotCommand(command="menu", description="🎛️ AntigravityTelegramAgent Control Center"),
        BotCommand(command="sentinel_add", description="🤖 Schedule Autonomous Sentinel Job"),
        BotCommand(command="sentinel_list", description="📋 List Active Sentinel Scheduled Jobs"),
        BotCommand(command="sentinel_remove", description="🗑️ Remove Sentinel Scheduled Job"),
        BotCommand(command="new", description="✨ Start new agent session (/new or /reset)"),
        BotCommand(command="usage", description="📊 AI limits and quotas (/usage)"),
        BotCommand(command="auth", description="🔑 View and Hot Reload Google account"),
        BotCommand(command="resume", description="📂 Resume session from history (/resume)"),
        BotCommand(command="rename", description="✏️ Rename current session (/rename New Name)"),
        BotCommand(command="mcp", description="🔌 Manage MCP servers (Memory, Search, CRM)"),
        BotCommand(command="models", description="🤖 Select AI model (Gemini, Claude, GPT)"),
        BotCommand(command="effort", description="⚡ Reasoning effort (low/medium/high)"),
        BotCommand(command="mode", description="🎯 Working mode (Plan / Auto-Edits / Standard)"),
        BotCommand(command="cd", description="📂 Change workspace directory"),
        BotCommand(command="reset", description="🔄 Reset agent session"),
        BotCommand(command="debug", description="🔍 Debug: session state and PTY"),
        BotCommand(command="start", description="👋 Help and start"),
    ])

    logger.info("🚀 Starting AntigravityTelegramAgent (Control Center & Pyte PTY Architecture)...")
    await dp.start_polling(bot)

if __name__ == "__main__":
    asyncio.run(main())
