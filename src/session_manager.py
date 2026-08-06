import logging
from typing import Dict
from src.cli_runner import AgySession

logger = logging.getLogger(__name__)

class SessionManager:
    """Singleton-like manager for active chat sessions."""
    def __init__(self):
        self.sessions: Dict[int, AgySession] = {}

    def get_session(self, chat_id: int) -> AgySession:
        if chat_id not in self.sessions:
            logger.info(f"Creating new session object for chat_id={chat_id}")
            self.sessions[chat_id] = AgySession(chat_id)
        return self.sessions[chat_id]

    def reset_session(self, chat_id: int) -> bool:
        if chat_id in self.sessions:
            session = self.sessions.pop(chat_id)
            session.close()
            return True
        return False

session_manager = SessionManager()
