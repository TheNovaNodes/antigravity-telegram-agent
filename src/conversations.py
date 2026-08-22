import sqlite3
import logging
from contextlib import closing
from pathlib import Path
from typing import List, Dict, Optional

logger = logging.getLogger(__name__)

DB_PATH = Path.home() / ".gemini" / "antigravity-cli" / "conversation_summaries.db"


def get_available_conversations(limit: int = 8) -> List[Dict[str, str]]:
    """Retrieve recent active agy conversations with summary, step count, and date."""
    conversations = []
    if not DB_PATH.exists():
        logger.warning(f"Conversation summaries DB not found at {DB_PATH}")
        return conversations

    try:
        with closing(sqlite3.connect(str(DB_PATH), timeout=5.0)) as conn, conn:
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

def rename_conversation(conversation_id: str, new_title: str) -> bool:
    """Manually rename a conversation title in the agy CLI SQLite database."""
    if not DB_PATH.exists() or not conversation_id:
        return False
    
    try:
        with closing(sqlite3.connect(str(DB_PATH), timeout=5.0)) as conn, conn:
            conn.execute(
                "UPDATE conversation_summaries SET title = ? WHERE conversation_id = ?",
                (new_title, conversation_id)
            )
            conn.commit()
            return True
    except Exception as e:
        logger.error(f"Failed to rename conversation {conversation_id}: {e}", exc_info=True)
        return False

def get_latest_conversation_id() -> Optional[str]:
    """Retrieve the most recent conversation_id from conversation_summaries.db."""
    if not DB_PATH.exists():
        return None
    try:
        with closing(sqlite3.connect(str(DB_PATH), timeout=5.0)) as conn, conn:
            row = conn.execute(
                "SELECT conversation_id FROM conversation_summaries ORDER BY last_modified_time DESC LIMIT 1"
            ).fetchone()
            if row:
                return row[0]
    except Exception as e:
        logger.error(f"Failed to fetch latest conversation ID: {e}", exc_info=True)
    return None

def get_conversation_title(conversation_id: str) -> Optional[str]:
    """Retrieve the summary or title of a specific conversation by its ID."""
    if not conversation_id or not DB_PATH.exists():
        return None
    try:
        with closing(sqlite3.connect(str(DB_PATH), timeout=5.0)) as conn, conn:
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
