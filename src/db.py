import os
import sqlite3
import logging
from pathlib import Path
from typing import Optional, Dict, Union
from contextlib import closing
import threading
from src.session_key import SessionKey

logger = logging.getLogger(__name__)

PROJECT_ROOT = Path(__file__).resolve().parent.parent
DATA_DIR = PROJECT_ROOT / "data"
DB_PATH = Path(os.environ.get("AG_TEST_DB_PATH", DATA_DIR / "antigravity-telegram-agent.db"))

_conn = None
_db_lock = threading.Lock()

def _enforce_db_permissions():
    """Ensure database file and WAL journal files have strict 0600 permissions."""
    if DB_PATH.exists():
        try:
            os.chmod(DB_PATH, 0o600)
        except Exception:
            pass
    for suffix in ("-wal", "-shm"):
        p = Path(str(DB_PATH) + suffix)
        if p.exists():
            try:
                os.chmod(p, 0o600)
            except Exception:
                pass

def _get_connection() -> sqlite3.Connection:
    """Create data directory and open a shared SQLite connection with WAL mode."""
    global _conn
    if _conn is None:
        DB_PATH.parent.mkdir(parents=True, exist_ok=True)
        _conn = sqlite3.connect(str(DB_PATH), timeout=10.0, check_same_thread=False)
        _conn.execute("PRAGMA journal_mode=WAL")
        _conn.execute("PRAGMA busy_timeout=5000")
        _conn.row_factory = sqlite3.Row
        _enforce_db_permissions()
    return _conn


def reset_db_connection():
    """Reset global DB connection (useful for tests changing DB_PATH)."""
    global _conn
    with _db_lock:
        if _conn:
            try:
                _conn.close()
            except Exception:
                pass
            _conn = None


def init_db(default_bot_id: int = 0):
    """Initialize database tables idempotently and run migrations for composite key (bot_id, chat_id)."""
    with _db_lock:
        conn = _get_connection()
        # Check existing table schema
        cursor = conn.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='user_sessions'")
        table_exists = cursor.fetchone() is not None

        if not table_exists:
            conn.execute("""
                CREATE TABLE user_sessions (
                    bot_id INTEGER NOT NULL DEFAULT 0,
                    chat_id INTEGER NOT NULL,
                    model_name TEXT NOT NULL,
                    effort TEXT NOT NULL,
                    mode TEXT NOT NULL,
                    conversation_id TEXT,
                    workspace TEXT,
                    updated_at TEXT DEFAULT (datetime('now')),
                    PRIMARY KEY (bot_id, chat_id)
                );
            """)
        else:
            cursor = conn.execute("PRAGMA table_info(user_sessions)")
            columns = {row["name"]: row for row in cursor.fetchall()}
            pk_cols = [name for name, info in columns.items() if info["pk"] > 0]

            # Migrate legacy table with single primary key `chat_id`
            if "bot_id" not in columns or pk_cols == ["chat_id"]:
                logger.info(f"Migrating user_sessions schema to composite PRIMARY KEY (bot_id, chat_id)...")
                conn.execute("ALTER TABLE user_sessions RENAME TO user_sessions_legacy;")
                conn.execute("""
                    CREATE TABLE user_sessions (
                        bot_id INTEGER NOT NULL DEFAULT 0,
                        chat_id INTEGER NOT NULL,
                        model_name TEXT NOT NULL,
                        effort TEXT NOT NULL,
                        mode TEXT NOT NULL,
                        conversation_id TEXT,
                        workspace TEXT,
                        updated_at TEXT DEFAULT (datetime('now')),
                        PRIMARY KEY (bot_id, chat_id)
                    );
                """)
                cursor = conn.execute("PRAGMA table_info(user_sessions_legacy)")
                legacy_cols = [row["name"] for row in cursor.fetchall()]
                conv_col = "conversation_id" if "conversation_id" in legacy_cols else "NULL"
                ws_col = "workspace" if "workspace" in legacy_cols else "NULL"
                conn.execute(f"""
                    INSERT OR IGNORE INTO user_sessions (bot_id, chat_id, model_name, effort, mode, conversation_id, workspace, updated_at)
                    SELECT ?, chat_id, model_name, effort, mode, {conv_col}, {ws_col}, updated_at
                    FROM user_sessions_legacy;
                """, (default_bot_id,))
                conn.execute("DROP TABLE user_sessions_legacy;")

        conn.commit()
        _enforce_db_permissions()
    logger.info("SQLite database initialized for multi-bot session persistence.")


def _resolve_session_key(key: Union[SessionKey, int], default_bot_id: int = 0) -> SessionKey:
    if isinstance(key, SessionKey):
        return key
    return SessionKey(bot_id=default_bot_id, chat_id=key)


def save_user_session(
    key: Union[SessionKey, int],
    model_name: str,
    effort: str,
    mode: str,
    conversation_id: Optional[str] = None,
    workspace: Optional[str] = None,
    default_bot_id: int = 0
):
    """Save or update user session settings in SQLite."""
    sk = _resolve_session_key(key, default_bot_id)
    try:
        with _db_lock:
            conn = _get_connection()
            conn.execute("""
                INSERT INTO user_sessions (bot_id, chat_id, model_name, effort, mode, conversation_id, workspace, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
                ON CONFLICT(bot_id, chat_id) DO UPDATE SET
                    model_name = excluded.model_name,
                    effort = excluded.effort,
                    mode = excluded.mode,
                    conversation_id = excluded.conversation_id,
                    workspace = excluded.workspace,
                    updated_at = datetime('now');
            """, (sk.bot_id, sk.chat_id, model_name, effort, mode, conversation_id, workspace))
            conn.commit()
    except Exception as e:
        logger.error(f"Failed to save user session for key={sk}: {e}", exc_info=True)


def load_user_session(key: Union[SessionKey, int], default_bot_id: int = 0) -> Optional[Dict[str, Optional[str]]]:
    """Load saved user session settings from SQLite."""
    sk = _resolve_session_key(key, default_bot_id)
    try:
        with _db_lock:
            conn = _get_connection()
            cursor = conn.execute(
                "SELECT model_name, effort, mode, conversation_id, workspace FROM user_sessions WHERE bot_id = ? AND chat_id = ?",
                (sk.bot_id, sk.chat_id)
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
        logger.error(f"Failed to load user session for key={sk}: {e}", exc_info=True)
    return None


def delete_user_session(key: Union[SessionKey, int], default_bot_id: int = 0):
    """Delete saved user session settings from SQLite."""
    sk = _resolve_session_key(key, default_bot_id)
    try:
        with _db_lock:
            conn = _get_connection()
            conn.execute("DELETE FROM user_sessions WHERE bot_id = ? AND chat_id = ?", (sk.bot_id, sk.chat_id))
            conn.commit()
    except Exception as e:
        logger.error(f"Failed to delete user session for key={sk}: {e}", exc_info=True)
