import sqlite3
import logging
from contextlib import closing
from pathlib import Path
from typing import List, Dict, Optional
from src.profile import BotProfile

logger = logging.getLogger(__name__)

DEFAULT_DB_PATH = Path.home() / ".gemini" / "antigravity-cli" / "conversation_summaries.db"


def _resolve_db_path(profile: Optional[BotProfile] = None) -> Path:
    """Resolve database path for profile, falling back to default if profile is None or name is default."""
    if profile and profile.name and profile.name != "default":
        prof_db = profile.state_dir / "conversation_summaries.db"
        if prof_db.exists():
            return prof_db
        # Return profile db path if state_dir exists so queries execute against profile db
        return prof_db
    return DEFAULT_DB_PATH


def get_available_conversations(limit: int = 8, profile: Optional[BotProfile] = None) -> List[Dict[str, str]]:
    """Retrieve recent active agy conversations with summary, step count, and date."""
    conversations = []
    db_path = _resolve_db_path(profile)

    if not db_path.exists():
        logger.warning(f"Conversation summaries DB not found at {db_path}")
        return conversations

    try:
        with closing(sqlite3.connect(str(db_path), timeout=5.0)) as conn, conn:
            conn.row_factory = sqlite3.Row
            rows = conn.execute("""
                SELECT conversation_id, preview, title, step_count, last_modified_time
                FROM conversation_summaries
                WHERE step_count > 0
                ORDER BY last_modified_time DESC
                LIMIT ?
            """, (limit,)).fetchall()

            for r in rows:
                summary = r["title"] or r["preview"] or "Untitled dialog"
                # Format short date (e.g. 07.08 06:09)
                date_str = ""
                if r["last_modified_time"]:
                    try:
                        # 2026-08-07 06:09:15...
                        raw_date = str(r["last_modified_time"])
                        parts = raw_date.split(" ")
                        if len(parts) >= 2:
                            date_parts = parts[0].split("-")
                            time_parts = parts[1].split(":")
                            if len(date_parts) == 3 and len(time_parts) >= 2:
                                date_str = f"{date_parts[2]}.{date_parts[1]} {time_parts[0]}:{time_parts[1]}"
                    except Exception:
                        date_str = str(r["last_modified_time"])[:16]

                conversations.append({
                    "id": r["conversation_id"],
                    "summary": summary,
                    "step_count": r["step_count"],
                    "date": date_str
                })
    except Exception as e:
        logger.error(f"Failed to fetch conversation summaries: {e}", exc_info=True)

    return conversations

def rename_conversation(conversation_id: str, new_title: str, profile: Optional[BotProfile] = None) -> bool:
    """Manually rename a conversation title in the agy CLI SQLite database."""
    db_path = _resolve_db_path(profile)
    if not db_path.exists() or not conversation_id:
        return False
    
    try:
        with closing(sqlite3.connect(str(db_path), timeout=5.0)) as conn, conn:
            conn.execute(
                "UPDATE conversation_summaries SET title = ? WHERE conversation_id = ?",
                (new_title, conversation_id)
            )
            conn.commit()
            return True
    except Exception as e:
        logger.error(f"Failed to rename conversation {conversation_id}: {e}", exc_info=True)
        return False

def get_latest_conversation_id(profile: Optional[BotProfile] = None) -> Optional[str]:
    """Retrieve the most recent conversation_id from conversation_summaries.db."""
    db_path = _resolve_db_path(profile)
    if not db_path.exists():
        return None
    try:
        with closing(sqlite3.connect(str(db_path), timeout=5.0)) as conn, conn:
            row = conn.execute(
                "SELECT conversation_id FROM conversation_summaries ORDER BY last_modified_time DESC LIMIT 1"
            ).fetchone()
            if row:
                return row[0]
    except Exception as e:
        logger.error(f"Failed to fetch latest conversation ID: {e}", exc_info=True)
    return None

def get_conversation_title(conversation_id: str, profile: Optional[BotProfile] = None) -> Optional[str]:
    """Retrieve the summary or title of a specific conversation by its ID."""
    db_path = _resolve_db_path(profile)
    if not conversation_id or not db_path.exists():
        return None
    try:
        with closing(sqlite3.connect(str(db_path), timeout=5.0)) as conn, conn:
            conn.row_factory = sqlite3.Row
            row = conn.execute(
                "SELECT preview, title FROM conversation_summaries WHERE conversation_id = ?",
                (conversation_id,)
            ).fetchone()
            if row:
                return row["title"] or row["preview"] or "Untitled dialog"
    except Exception as e:
        logger.error(f"Failed to fetch conversation title for {conversation_id}: {e}", exc_info=True)
    return None
