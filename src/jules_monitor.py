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
                        
                        title = "✅ <b>Jules Task Completed</b>" if state == "COMPLETED" else f"🚨 <b>Jules Task {state}</b>"
                        prompt = session_info.get("prompt", session_info.get("title", "N/A"))
                        source = session_info.get("source", "N/A")
                        
                        message = (
                            f"{title}\n\n"
                            f"📌 <b>Session:</b> <code>{session_name}</code>\n"
                            f"📁 <b>Source:</b> <code>{source}</code>\n"
                            f"📝 <b>Prompt:</b> <i>{prompt}</i>\n"
                        )
                        
                        if "error" in session_info:
                            message += f"\n❌ <b>Error:</b> {session_info['error']}"
                            
                        patch_text = None
                        artifacts_summary = []
                        
                        try:
                            artifacts_resp = await client.list_artifacts(session_name)
                            artifacts = artifacts_resp.get("artifacts", [])
                            for art in artifacts:
                                name = art.get("name", "Artifact")
                                art_type = art.get("type", "")
                                artifacts_summary.append(f"• <code>{name}</code> ({art_type})")
                                if art_type in ("PATCH", "GIT_DIFF") or name.endswith((".patch", ".diff")):
                                    content_resp = await client.get_artifact_content(art["name"])
                                    patch_text = content_resp.get("patch") or content_resp.get("content")
                        except Exception as art_err:
                            logger.warning(f"Could not fetch artifacts for {session_name}: {art_err}")

                        if artifacts_summary:
                            message += "\n📦 <b>Artifacts:</b>\n" + "\n".join(artifacts_summary)

                        await bot.send_message(chat_id=chat_id, text=message, parse_mode="HTML")
                        
                        if patch_text:
                            if len(patch_text) <= 3000:
                                patch_msg = f"📄 <b>Git Patch:</b>\n<pre><code class=\"language-diff\">{patch_text}</code></pre>"
                                await bot.send_message(chat_id=chat_id, text=patch_msg, parse_mode="HTML")
                            else:
                                from aiogram.types import BufferedInputFile
                                patch_bytes = patch_text.encode("utf-8")
                                file_name = f"{session_name.replace('/', '_')}.patch"
                                doc = BufferedInputFile(patch_bytes, filename=file_name)
                                await bot.send_document(
                                    chat_id=chat_id,
                                    document=doc,
                                    caption=f"📄 Git Patch for session <code>{session_name}</code>",
                                    parse_mode="HTML"
                                )
                        
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
