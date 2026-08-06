import json
import logging
from pathlib import Path
from datetime import datetime, timezone

logger = logging.getLogger(__name__)

PROJECT_ROOT = Path(__file__).resolve().parent.parent
LOGS_DIR = PROJECT_ROOT / "logs"
AUDIT_LOG_PATH = LOGS_DIR / "audit.log"


def log_audit_event(user_id: int, chat_id: int, model_name: str, effort: str, mode: str, prompt: str, response_length: int):
    """Record structured JSON audit log entry for security and telemetry."""
    try:
        LOGS_DIR.mkdir(parents=True, exist_ok=True)
        entry = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "user_id": user_id,
            "chat_id": chat_id,
            "model_name": model_name,
            "effort": effort,
            "mode": mode,
            "prompt": prompt[:200] + "..." if len(prompt) > 200 else prompt,
            "prompt_length": len(prompt),
            "response_length": response_length
        }
        with open(AUDIT_LOG_PATH, "a", encoding="utf-8") as f:
            f.write(json.dumps(entry, ensure_ascii=False) + "\n")
    except Exception as e:
        logger.error(f"Failed to write audit log: {e}", exc_info=True)
