import asyncio
import logging
from aiogram import Bot
from src.jules_client import JulesClient

logger = logging.getLogger(__name__)

# In-memory tracking: session_name -> chat_id
ACTIVE_JULES_SESSIONS = {}

async def monitor_jules_sessions(bot: Bot, interval_seconds: int = 15):
    """Background task to poll active Jules sessions and notify users upon completion."""
    logger.info("Starting Jules sessions monitor...")
    client = JulesClient()
    
    while True:
        try:
            await asyncio.sleep(interval_seconds)
            
            # Use a list to avoid dictionary changed size during iteration
            for session_name, chat_id in list(ACTIVE_JULES_SESSIONS.items()):
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
                        ACTIVE_JULES_SESSIONS.pop(session_name, None)
                        
                except Exception as e:
                    logger.error(f"Error checking Jules session {session_name}: {e}")
                    
        except asyncio.CancelledError:
            logger.info("Jules sessions monitor stopped.")
            break
        except Exception as e:
            logger.error(f"Error in Jules monitor loop: {e}", exc_info=True)
