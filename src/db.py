import os
import sqlite3
import logging
from pathlib import Path
from typing import Optional, Dict

logger = logging.getLogger(__name__)

PROJECT_ROOT = Path(__file__).resolve().parent.parent
DATA_DIR = PROJECT_ROOT / "data"
DB_PATH = DATA_DIR / "dmagybot.db"


def _get_connection() -> sqlite3.Connection:
    """Create data directory and open SQLite connection with WAL mode."""
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(DB_PATH), timeout=10.0)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=5000")
    conn.row_factory = sqlite3.Row
    return conn


def init_db():
    """Initialize database tables idempotently and run migrations."""
    with _get_connection() as conn:
        conn.execute("""
            CREATE TABLE IF NOT EXISTS user_sessions (
                chat_id INTEGER PRIMARY KEY,
                model_name TEXT NOT NULL,
                effort TEXT NOT NULL,
                mode TEXT NOT NULL,
                conversation_id TEXT,
                updated_at TEXT DEFAULT (datetime('now'))
            );
        """)
        # Idempotently check if conversation_id and workspace columns exist
        cursor = conn.execute("PRAGMA table_info(user_sessions)")
        columns = [row["name"] for row in cursor.fetchall()]
        if "conversation_id" not in columns:
            conn.execute("ALTER TABLE user_sessions ADD COLUMN conversation_id TEXT;")
        if "workspace" not in columns:
            conn.execute("ALTER TABLE user_sessions ADD COLUMN workspace TEXT;")
        conn.commit()
    logger.info("SQLite database initialized for session & conversation persistence.")


def save_user_session(chat_id: int, model_name: str, effort: str, mode: str, conversation_id: Optional[str] = None, workspace: Optional[str] = None):
    """Save or update user session settings in SQLite."""
    try:
        with _get_connection() as conn:
            conn.execute("""
                INSERT INTO user_sessions (chat_id, model_name, effort, mode, conversation_id, workspace, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
                ON CONFLICT(chat_id) DO UPDATE SET
                    model_name = excluded.model_name,
                    effort = excluded.effort,
                    mode = excluded.mode,
                    conversation_id = excluded.conversation_id,
                    workspace = excluded.workspace,
                    updated_at = datetime('now');
            """, (chat_id, model_name, effort, mode, conversation_id, workspace))
            conn.commit()
        logger.debug(f"Saved session settings to DB for chat_id={chat_id}: model={model_name}, effort={effort}, mode={mode}, conv_id={conversation_id}, workspace={workspace}")
    except Exception as e:
        logger.error(f"Failed to save user session for chat_id={chat_id}: {e}", exc_info=True)


def load_user_session(chat_id: int) -> Optional[Dict[str, Optional[str]]]:
    """Load saved user session settings from SQLite."""
    try:
        with _get_connection() as conn:
            cursor = conn.execute(
                "SELECT model_name, effort, mode, conversation_id, workspace FROM user_sessions WHERE chat_id = ?",
                (chat_id,)
            )
            row = cursor.fetchone()
            if row:
                return {
                    "model_name": row["model_name"],
                    "effort": row["effort"],
                    "mode": row["mode"],
                    "conversation_id": row["conversation_id"] if "conversation_id" in row.keys() else None,
                    "workspace": row["workspace"] if "workspace" in row.keys() else None
                }
    except Exception as e:
        logger.error(f"Failed to load user session for chat_id={chat_id}: {e}", exc_info=True)
    return None


def delete_user_session(chat_id: int):
    """Delete saved user session settings from SQLite."""
    try:
        with _get_connection() as conn:
            conn.execute("DELETE FROM user_sessions WHERE chat_id = ?", (chat_id,))
            conn.commit()
        logger.debug(f"Deleted session from DB for chat_id={chat_id}")
    except Exception as e:
        logger.error(f"Failed to delete user session for chat_id={chat_id}: {e}", exc_info=True)
