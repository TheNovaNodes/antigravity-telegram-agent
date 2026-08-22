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
        self.cli_state_dir: Path = self.state_dir / ".gemini" / "antigravity-cli"
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

        self.cli_state_dir.mkdir(parents=True, exist_ok=True)
        try:
            self.cli_state_dir.chmod(0o700)
        except Exception:
            pass

        # Create brain and artifacts directories inside cli_state_dir
        for sub_dir in [self.cli_state_dir / "brain", self.cli_state_dir / "artifacts"]:
            sub_dir.mkdir(parents=True, exist_ok=True)
            try:
                sub_dir.chmod(0o700)
            except Exception:
                pass

        # Maintain legacy brain symlink/dir at state_dir level if needed for backward compatibility
        legacy_brain = self.state_dir / "brain"
        if not legacy_brain.exists():
            try:
                legacy_brain.symlink_to(self.cli_state_dir / "brain")
            except Exception:
                legacy_brain.mkdir(parents=True, exist_ok=True)

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


def migrate_legacy_shared_state(
    target_profile: Optional[BotProfile] = None,
    legacy_dir: Optional[Path] = None
) -> dict:
    """Migrates legacy shared state files/dirs into target profile's cli_state_dir.
    Creates a timestamped backup copy (.bak_<timestamp>) BEFORE copying/moving files.
    Returns a transactional status dict.
    """
    import datetime
    import json
    import tempfile

    if target_profile is None:
        target_profile = BotProfile("default")

    if legacy_dir is None:
        legacy_dir = Path.home() / ".gemini" / "antigravity-cli"

    timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S_%f")
    items_to_migrate = [
        "conversation_summaries.db",
        "user_sessions.db",
        "mcp_config.json",
        "antigravity-oauth-token",
        "settings.json",
        "brain",
        "artifacts",
    ]

    status = {
        "timestamp": timestamp,
        "target_profile": target_profile.name,
        "migrated": [],
        "backed_up": [],
        "skipped": {
            "missing_source": [],
            "idempotent_existing": []
        },
        "errors": []
    }

    if not legacy_dir.exists():
        logger.debug(f"Legacy directory '{legacy_dir}' does not exist. Skipping migration.")
        status["status"] = "SUCCESS"
        status_path = target_profile.cli_state_dir / "migration_status.json"
        try:
            status_path.write_text(json.dumps(status, indent=2))
            os.chmod(status_path, 0o600)
        except Exception as e:
            logger.error(f"Failed to write migration status log: {e}")
        return status

    for item_name in items_to_migrate:
        legacy_item = legacy_dir / item_name
        target_item = target_profile.cli_state_dir / item_name

        if not legacy_item.exists():
            status["skipped"]["missing_source"].append(item_name)
            continue

        if target_item.exists():
            if target_item.is_dir() and not any(target_item.iterdir()):
                # Empty directory created during BotProfile init, allow copying into it
                pass
            else:
                status["skipped"]["idempotent_existing"].append(item_name)
                continue

        backup_item = legacy_dir / f"{item_name}.bak_{timestamp}"

        try:
            # Step 1: Create backup copy
            if legacy_item.is_dir():
                shutil.copytree(legacy_item, backup_item, symlinks=True)
            else:
                shutil.copy2(legacy_item, backup_item)

            if backup_item.exists():
                try:
                    if backup_item.is_file():
                        os.chmod(backup_item, 0o600)
                    elif backup_item.is_dir():
                        os.chmod(backup_item, 0o700)
                except Exception:
                    pass
                status["backed_up"].append(str(backup_item.name))

            # Step 2: Copy to profile cli_state_dir
            if legacy_item.is_dir():
                shutil.copytree(legacy_item, target_item, symlinks=True, dirs_exist_ok=True)
            else:
                shutil.copy2(legacy_item, target_item)

            if target_item.exists():
                try:
                    if target_item.is_file():
                        target_profile.enforce_file_permissions(target_item)
                    elif target_item.is_dir():
                        os.chmod(target_item, 0o700)
                except Exception as perm_err:
                    logger.debug(f"Permission setting error for {target_item}: {perm_err}")

                status["migrated"].append(item_name)
                logger.info(f"Successfully migrated '{item_name}' to profile '{target_profile.name}'")
        except Exception as e:
            logger.error(f"Error migrating legacy state item '{item_name}': {e}")
            status["errors"].append({"item": item_name, "error": str(e)})

    # Post-migration: recursively enforce permissions on target_profile.state_dir
    if target_profile.state_dir.exists():
        for root, dirs, files in os.walk(target_profile.state_dir):
            for d in dirs:
                try:
                    os.chmod(os.path.join(root, d), 0o700)
                except Exception as perm_err:
                    logger.debug(f"Permission setting error for dir {d}: {perm_err}")
            for f in files:
                try:
                    os.chmod(os.path.join(root, f), 0o600)
                except Exception as perm_err:
                    logger.debug(f"Permission setting error for file {f}: {perm_err}")

    # Set overall migration status
    if len(status["errors"]) == 0:
        status["status"] = "SUCCESS"
    else:
        status["status"] = "FAILED"

    # Journal migration status log to migration_status.json
    status_path = target_profile.cli_state_dir / "migration_status.json"
    try:
        status_path.write_text(json.dumps(status, indent=2))
        os.chmod(status_path, 0o600)
    except Exception as e:
        logger.error(f"Failed to write migration status log: {e}")

    # Write .migration_complete marker file ONLY IF zero errors and status is SUCCESS
    if len(status["errors"]) == 0 and status["status"] == "SUCCESS":
        try:
            marker = target_profile.cli_state_dir / ".migration_complete"
            fd, tmp_marker_path = tempfile.mkstemp(dir=str(target_profile.cli_state_dir), prefix=".tmp_marker_")
            with os.fdopen(fd, 'w') as f:
                f.write(f"migrated_at={timestamp}\n")
            os.chmod(tmp_marker_path, 0o600)
            os.replace(tmp_marker_path, marker)
        except Exception as marker_err:
            logger.error(f"Failed to atomically write migration marker: {marker_err}")

    return status
