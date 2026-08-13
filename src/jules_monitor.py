import asyncio
import logging
import os
from aiogram import Bot
from src.jules_client import JulesClient

logger = logging.getLogger(__name__)

# In-memory tracking: session_name -> chat_id
ACTIVE_JULES_SESSIONS = {}
ACTIVE_JULES_SESSIONS_LOCK = asyncio.Lock()

async def monitor_jules_sessions(bot: Bot, interval_seconds: int = 15):
    """Background task to poll active Jules sessions and notify users upon completion."""
    if not os.environ.get("JULES_API_KEY"):
        logger.warning("JULES_API_KEY is not set. Jules monitor will not start.")
        return

    logger.info("Starting Jules sessions monitor...")
    client = JulesClient()
    
    while True:
        try:
            await asyncio.sleep(interval_seconds)
            
            async with ACTIVE_JULES_SESSIONS_LOCK:
                sessions_copy = list(ACTIVE_JULES_SESSIONS.items())
            
            for session_name, chat_id in sessions_copy:
                try:
                    session_info = await client.get_session(session_name)
                    state = session_info.get("state")
                    
                    if state in ("COMPLETED", "FAILED", "ERROR"):
                        logger.info(f"Jules session {session_name} reached terminal state: {state}")
                        
                        message = f"🔔 <b>Jules Session Update</b>\n\nSession: <code>{session_name}</code>\nStatus: <b>{state}</b>"
                        
                        if "error" in session_info:
                            message += f"\nError: {session_info['error']}"
                            
                        await bot.send_message(chat_id=chat_id, text=message, parse_mode="HTML")
                        
                        # Remove from tracking
                        async with ACTIVE_JULES_SESSIONS_LOCK:
                            ACTIVE_JULES_SESSIONS.pop(session_name, None)
                        
                except Exception as e:
                    logger.error(f"Error checking Jules session {session_name}: {e}")
                    
        except asyncio.CancelledError:
            logger.info("Jules sessions monitor stopped.")
            break
        except Exception as e:
            logger.error(f"Error in Jules monitor loop: {e}", exc_info=True)
