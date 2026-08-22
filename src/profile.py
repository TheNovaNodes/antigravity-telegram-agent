import os
import re
import shutil
import logging
from pathlib import Path
from typing import Union, Optional

logger = logging.getLogger(__name__)

PROFILES_ROOT = Path.home() / ".gemini" / "antigravity-cli" / "profiles"


def sanitize_profile_name(name: str) -> str:
    """Sanitize profile name into a safe filesystem slug."""
    s = re.sub(r'[^a-zA-Z0-9_-]', '_', name.strip())
    s = re.sub(r'_+', '_', s).strip('_')
    return s if s else "default"


class BotProfile:
    """Represents an isolated bot profile with dedicated state directory and configurations."""

    def __init__(self, name: str, bot_id: Optional[int] = None, role_name: Optional[str] = None):
        allowed_root = PROFILES_ROOT.resolve()

        # Strict path traversal & symlink escape checks on raw name input before sanitizing
        raw_target_dir = (allowed_root / name).resolve()
        if raw_target_dir.is_symlink():
            raise ValueError(f"Symlink escape detected for profile name '{name}': '{raw_target_dir}' is a symlink")
        try:
            raw_target_dir.relative_to(allowed_root)
        except ValueError:
            raise ValueError(f"Path traversal detected for profile name '{name}': '{raw_target_dir}' escapes allowed root '{allowed_root}'")

        sanitized_name = sanitize_profile_name(name)
        if not sanitized_name:
            sanitized_name = "default"
        self.name: str = sanitized_name
        self.bot_id: Optional[int] = bot_id
        self.role_name: str = role_name or self.name

        target_dir = (allowed_root / self.name).resolve()
        if target_dir.is_symlink():
            raise ValueError(f"Symlink escape detected for target profile dir '{self.name}'")
        try:
            target_dir.relative_to(allowed_root)
        except ValueError:
            raise ValueError(f"Path traversal detected for profile name '{name}': '{target_dir}' escapes allowed root '{allowed_root}'")

        self.state_dir: Path = target_dir
        self.system_prompt_path: Path = self.state_dir / "SOUL.md"

        # Initialize directory structure with restrictive permissions (0700)
        self.ensure_state_dir()

    def ensure_state_dir(self) -> None:
        """Create state directory structure with strict 0700 permissions."""
        PROFILES_ROOT.mkdir(parents=True, exist_ok=True)
        try:
            PROFILES_ROOT.chmod(0o700)
        except Exception:
            pass

        self.state_dir.mkdir(parents=True, exist_ok=True)
        try:
            self.state_dir.chmod(0o700)
        except Exception:
            pass

        # Create brain directory inside profile
        brain_dir = self.state_dir / "brain"
        brain_dir.mkdir(parents=True, exist_ok=True)
        try:
            brain_dir.chmod(0o700)
        except Exception:
            pass

    def enforce_file_permissions(self, file_path: Union[str, Path]) -> None:
        """Enforce restrictive permissions (0600) for profile state files."""
        path = Path(file_path).resolve()
        if path.is_symlink():
            raise ValueError(f"Symlink file permissions check rejected for '{path}'")
        try:
            path.relative_to(self.state_dir.resolve())
        except ValueError:
            raise ValueError(f"File '{path}' is outside profile state directory '{self.state_dir}'")

        if path.exists() and path.is_file():
            path.chmod(0o600)


def migrate_legacy_shared_state(target_profile: Optional[BotProfile] = None) -> None:
    """Migrates legacy shared state (e.g. conversation_summaries.db) into profile state directory.
    Backs up legacy DB with .bak before moving to target profile state dir.
    """
    if target_profile is None:
        target_profile = BotProfile("default")

    legacy_dir = Path.home() / ".gemini" / "antigravity-cli"
    legacy_db = legacy_dir / "conversation_summaries.db"
    target_db = target_profile.state_dir / "conversation_summaries.db"

    if legacy_db.exists() and not target_db.exists():
        logger.info(f"Migrating legacy DB '{legacy_db}' to profile '{target_profile.name}' at '{target_db}'")
        backup_db = legacy_dir / "conversation_summaries.db.bak"
        try:
            shutil.copy2(legacy_db, backup_db)
            os.chmod(backup_db, 0o600)
            shutil.move(legacy_db, target_db)
            target_profile.enforce_file_permissions(target_db)
            logger.info("Legacy DB migration completed successfully.")
        except Exception as e:
            logger.error(f"Error during legacy shared state migration: {e}")
