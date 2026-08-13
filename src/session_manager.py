import asyncio
import time
import logging
from typing import Dict
from src.cli_runner import AgySession
from src.db import load_user_session, delete_user_session, save_user_session

logger = logging.getLogger(__name__)

class SessionManager:
    """Singleton-like manager for active chat sessions with SQLite persistence and idle cleanup."""
    def __init__(self, idle_ttl_seconds: int = 1800):
        self.sessions: Dict[int, AgySession] = {}
        self.last_accessed: Dict[int, float] = {}
        self.idle_ttl_seconds = idle_ttl_seconds
        self._cleanup_task = None

    def get_session(self, chat_id: int) -> AgySession:
        self.last_accessed[chat_id] = time.time()
        if chat_id not in self.sessions:
            # Check SQLite DB for previously saved user settings across deployments
            saved = load_user_session(chat_id)
            if saved:
                logger.info(f"Restored session configuration from DB for chat_id={chat_id}: {saved}")
                session = AgySession(
                    chat_id,
                    model_name=saved.get("model_name", "gemini-3.1-pro-high"),
                    effort=saved.get("effort", "high"),
                    mode=saved.get("mode", "default"),
                    conversation_id=saved.get("conversation_id"),
                    workspace=saved.get("workspace")
                )
            else:
                logger.info(f"Creating new default session object for chat_id={chat_id}")
                session = AgySession(chat_id)
                # Persist initial session defaults to SQLite DB
                save_user_session(chat_id, session.model_name, session.effort, session.mode, session.conversation_id, session.workspace)
            self.sessions[chat_id] = session
        return self.sessions[chat_id]

    def new_session(self, chat_id: int) -> 'AgySession':
        """Create a new conversation or reset existing one while preserving user preferences."""
        session = self.sessions.get(chat_id)
        if session:
            session.clear_context()
            save_user_session(chat_id, session.model_name, session.effort, session.mode, session.conversation_id, session.workspace)
            self.last_accessed[chat_id] = time.time()
            return session
        else:
            saved = load_user_session(chat_id)
            preserved = {
                "model_name": saved.get("model_name", "gemini-3.6-flash-low") if saved else "gemini-3.6-flash-low",
                "effort": saved.get("effort", "low") if saved else "low",
                "mode": saved.get("mode", "default") if saved else "default",
                "workspace": saved.get("workspace") if saved else None,
            }

        new = AgySession(
            chat_id,
            model_name=preserved["model_name"],
            effort=preserved["effort"],
            mode=preserved["mode"],
            conversation_id=None,
            workspace=preserved["workspace"]
        )
        self.sessions[chat_id] = new
        self.last_accessed[chat_id] = time.time()
        save_user_session(chat_id, new.model_name, new.effort, new.mode, None, new.workspace)
        logger.info(f"Created new session for chat_id={chat_id} preserving preferences: model={new.model_name}, effort={new.effort}, mode={new.mode}")
        return new

    def reset_session(self, chat_id: int) -> bool:
        """Full reset: delete all saved preferences and close PTY. Use new_session() for /new instead."""
        delete_user_session(chat_id)
        self.last_accessed.pop(chat_id, None)
        if chat_id in self.sessions:
            session = self.sessions.pop(chat_id)
            session.close()
            return True
        return False

    def cleanup_idle_sessions(self):
        """Close PTY processes for sessions idle longer than idle_ttl_seconds."""
        if self.idle_ttl_seconds <= 0:
            return  # Disabled, run permanently

        now = time.time()
        expired_chats = [
            chat_id for chat_id, last_time in self.last_accessed.items()
            if now - last_time > self.idle_ttl_seconds
        ]
        for chat_id in expired_chats:
            logger.info(f"Closing idle session for chat_id={chat_id} (idle > {self.idle_ttl_seconds}s)")
            self.last_accessed.pop(chat_id, None)
            session = self.sessions.pop(chat_id, None)
            if session:
                session.close()

    async def start_cleanup_loop(self, check_interval_seconds: int = 300):
        """Background loop to periodically cleanup idle PTY sessions."""
        logger.info("Starting background idle session cleanup loop...")
        while True:
            try:
                await asyncio.sleep(check_interval_seconds)
                self.cleanup_idle_sessions()
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error(f"Error in idle session cleanup loop: {e}", exc_info=True)

session_manager = SessionManager(idle_ttl_seconds=0)


