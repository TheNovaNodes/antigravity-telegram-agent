import os
import re
from pathlib import Path
from typing import Union, Optional

PROFILES_ROOT = Path.home() / ".gemini" / "antigravity-cli" / "profiles"


def sanitize_profile_name(name: str) -> str:
    """Sanitize profile name into a safe filesystem slug."""
    s = re.sub(r'[^a-zA-Z0-9_-]', '_', name.strip())
    s = re.sub(r'_+', '_', s).strip('_')
    return s if s else "default"


class BotProfile:
    """Represents an isolated bot profile with dedicated state directory and configurations."""

    def __init__(self, name: str, bot_id: Optional[int] = None, role_name: Optional[str] = None):
        # Strict path traversal protection on raw name input before sanitizing
        allowed_root = PROFILES_ROOT.resolve()
        raw_target_dir = (allowed_root / name).resolve()
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
        try:
            path.relative_to(self.state_dir.resolve())
        except ValueError:
            raise ValueError(f"File '{path}' is outside profile state directory '{self.state_dir}'")

        if path.exists() and path.is_file():
            path.chmod(0o600)
