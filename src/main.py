import asyncio
import logging
import os
import sys

# Ensure project root is in sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiogram import Bot, Dispatcher
from aiogram.types import BotCommand

from src.config import BOT_TOKENS, LOG_LEVEL
from src.db import init_db
from src.handlers import router
from src.session_manager import session_manager
from src.jules_monitor import monitor_jules_sessions
from src.scheduler import sentinel_scheduler
from src.bot_registry import bot_registry

logging.basicConfig(level=getattr(logging, LOG_LEVEL))
logger = logging.getLogger(__name__)

async def main():
    bots = [Bot(token=token) for token in BOT_TOKENS]
    for b in bots:
        await b.get_me()
        bot_registry.register(b)

    default_bot_id = bots[0].id if bots else 0

    # Initialize SQLite DB for persistent session state across deployments with primary bot_id
    init_db(default_bot_id=default_bot_id)

    # Start background idle session cleanup loop (every 5 mins, 30 min TTL)
    asyncio.create_task(session_manager.start_cleanup_loop())

    # Start Autonomous Sentinel Scheduler
    sentinel_scheduler.start()
    
    # Start Jules sessions monitor loop with primary bot
    asyncio.create_task(monitor_jules_sessions(bots[0]))

    dp = Dispatcher()
    dp.include_router(router)

    commands = [
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
    ]

    for b in bots:
        await b.set_my_commands(commands)

    logger.info(f"🚀 Starting AntigravityTelegramAgent with {len(bots)} bot instance(s) under single Dispatcher...")
    await dp.start_polling(*bots)

if __name__ == "__main__":
    asyncio.run(main())
