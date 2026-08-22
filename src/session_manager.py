import asyncio
import time
import logging
from typing import Dict, Union, Optional
from src.session_key import SessionKey
from src.cli_runner import AgySession
from src.db import load_user_session, delete_user_session, save_user_session

logger = logging.getLogger(__name__)

class SessionManager:
    """Singleton-like manager for active chat sessions with SQLite persistence and idle cleanup."""
    def __init__(self, idle_ttl_seconds: int = 1800):
        self.sessions: Dict[SessionKey, AgySession] = {}
        self.last_accessed: Dict[SessionKey, float] = {}
        self.idle_ttl_seconds = idle_ttl_seconds
        self._cleanup_task = None

    def _resolve_key(self, key: Union[SessionKey, int], bot_id: int = 0) -> SessionKey:
        if isinstance(key, SessionKey):
            return key
        return SessionKey(bot_id=bot_id, chat_id=key)

    def get_session(self, key: Union[SessionKey, int], bot_id: int = 0) -> AgySession:
        sk = self._resolve_key(key, bot_id)
        self.last_accessed[sk] = time.time()
        if sk not in self.sessions:
            # Check SQLite DB for previously saved user settings across deployments
            saved = load_user_session(sk)
            from src.config import get_profile_for_bot
            profile = get_profile_for_bot(sk.bot_id)
            if saved:
                logger.info(f"Restored session configuration from DB for key={sk}: {saved}")
                session = AgySession(
                    sk.chat_id,
                    model_name=saved.get("model_name", "gemini-3.6-flash-low"),
                    effort=saved.get("effort", "low"),
                    mode=saved.get("mode", "default"),
                    conversation_id=saved.get("conversation_id"),
                    workspace=saved.get("workspace"),
                    session_key=sk,
                    profile=profile
                )
            else:
                logger.info(f"Creating new default session object for key={sk}")
                session = AgySession(sk.chat_id, session_key=sk, profile=profile)
                # Persist initial session defaults to SQLite DB
                save_user_session(sk, session.model_name, session.effort, session.mode, session.conversation_id, session.workspace)
            self.sessions[sk] = session
        return self.sessions[sk]

    def new_session(self, key: Union[SessionKey, int], bot_id: int = 0) -> AgySession:
        """Create a new conversation or reset existing one while preserving user preferences."""
        sk = self._resolve_key(key, bot_id)
        session = self.sessions.get(sk)
        if session:
            session.clear_context()
            save_user_session(sk, session.model_name, session.effort, session.mode, session.conversation_id, session.workspace)
            self.last_accessed[sk] = time.time()
            return session
        else:
            saved = load_user_session(sk)
            preserved = {
                "model_name": saved.get("model_name", "gemini-3.6-flash-low") if saved else "gemini-3.6-flash-low",
                "effort": saved.get("effort", "low") if saved else "low",
                "mode": saved.get("mode", "default") if saved else "default",
                "workspace": saved.get("workspace") if saved else None,
            }

        from src.config import get_profile_for_bot
        profile = get_profile_for_bot(sk.bot_id)
        new = AgySession(
            sk.chat_id,
            model_name=preserved["model_name"],
            effort=preserved["effort"],
            mode=preserved["mode"],
            conversation_id=None,
            workspace=preserved["workspace"],
            session_key=sk,
            profile=profile
        )
        self.sessions[sk] = new
        self.last_accessed[sk] = time.time()
        save_user_session(sk, new.model_name, new.effort, new.mode, None, new.workspace)
        logger.info(f"Created new session for key={sk} preserving preferences: model={new.model_name}, effort={new.effort}, mode={new.mode}")
        return new

    def reset_session(self, key: Union[SessionKey, int], bot_id: int = 0) -> bool:
        """Full reset: delete all saved preferences and close PTY. Use new_session() for /new instead."""
        sk = self._resolve_key(key, bot_id)
        delete_user_session(sk)
        self.last_accessed.pop(sk, None)
        if sk in self.sessions:
            session = self.sessions.pop(sk)
            session.close()
            return True
        return False

    def cleanup_idle_sessions(self):
        """Close PTY processes for sessions idle longer than idle_ttl_seconds."""
        if self.idle_ttl_seconds <= 0:
            return  # Disabled, run permanently

        now = time.time()
        expired_keys = [
            sk for sk, last_time in self.last_accessed.items()
            if now - last_time > self.idle_ttl_seconds
        ]
        for sk in expired_keys:
            logger.info(f"Closing idle session for key={sk} (idle > {self.idle_ttl_seconds}s)")
            self.last_accessed.pop(sk, None)
            session = self.sessions.pop(sk, None)
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

session_manager = SessionManager(idle_ttl_seconds=1800)
