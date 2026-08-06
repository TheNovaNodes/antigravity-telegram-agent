import logging
from typing import Dict
from src.cli_runner import AgySession
from src.db import load_user_session, delete_user_session, save_user_session

logger = logging.getLogger(__name__)

class SessionManager:
    """Singleton-like manager for active chat sessions with SQLite persistence."""
    def __init__(self):
        self.sessions: Dict[int, AgySession] = {}

    def get_session(self, chat_id: int) -> AgySession:
        if chat_id not in self.sessions:
            # Check SQLite DB for previously saved user settings across deployments
            saved = load_user_session(chat_id)
            if saved:
                logger.info(f"Restored session configuration from DB for chat_id={chat_id}: {saved}")
                session = AgySession(
                    chat_id,
                    model_name=saved.get("model_name", "gemini-3.1-pro-high"),
                    effort=saved.get("effort", "high"),
                    mode=saved.get("mode", "default")
                )
            else:
                logger.info(f"Creating new default session object for chat_id={chat_id}")
                session = AgySession(chat_id)
                # Persist initial session defaults to SQLite DB
                save_user_session(chat_id, session.model_name, session.effort, session.mode)
            self.sessions[chat_id] = session
        return self.sessions[chat_id]

    def reset_session(self, chat_id: int) -> bool:
        delete_user_session(chat_id)
        if chat_id in self.sessions:
            session = self.sessions.pop(chat_id)
            session.close()
            return True
        return False

session_manager = SessionManager()

