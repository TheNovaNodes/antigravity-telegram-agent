import asyncio
import hashlib
import logging
import os
from pathlib import Path
from typing import Optional
import pexpect
import pyte
from src.config import AGY_BINARY_PATH
from src.db import save_user_session
from src.mcp_config import mcp_config
from src.formatters import format_dyslexia_friendly_text

logger = logging.getLogger(__name__)

AVAILABLE_MODELS = {
    "gemini-flash-high": "gemini-3.6-flash-high",
    "gemini-flash-medium": "gemini-3.6-flash-medium",
    "gemini-flash-low": "gemini-3.6-flash-low",
    "gemini-pro-high": "gemini-3.1-pro-high",
    "gemini-pro-low": "gemini-3.1-pro-low",
    "claude-sonnet": "claude-sonnet-4-6",
    "claude-opus": "claude-opus-4-6-thinking",
    "gpt-oss": "gpt-oss-120b-medium"
}

AVAILABLE_EFFORTS = ["low", "medium", "high"]
AVAILABLE_MODES = {"default": "Standard Chat", "plan": "Planning Mode", "accept-edits": "Auto-Edits Mode"}


import signal

import urllib.request
import json

def get_active_account_email() -> str:
    """Retrieve the currently authenticated Google account email via Google OAuth userinfo endpoint."""
    home = Path.home()
    token_file = home / ".gemini" / "antigravity-cli" / "antigravity-oauth-token"
    if token_file.exists():
        try:
            data = json.loads(token_file.read_text())
            access_token = data.get("token", {}).get("access_token")
            if access_token:
                req = urllib.request.Request("https://www.googleapis.com/oauth2/v3/userinfo")
                req.add_header("Authorization", f"Bearer {access_token}")
                with urllib.request.urlopen(req, timeout=3.0) as resp:
                    info = json.loads(resp.read().decode())
                    email = info.get("email")
                    if email:
                        return email
        except Exception as e:
            logger.warning(f"Failed to fetch userinfo via OAuth token: {e}")

    # Fallback scan of agy logs
    log_dir = home / ".gemini" / "antigravity-cli" / "log"
    if log_dir.exists():
        try:
            logs = sorted(log_dir.glob("cli-*.log"), key=lambda f: f.stat().st_mtime, reverse=True)
            if logs:
                content = logs[0].read_text(errors="ignore")
                match = re.search(r"authenticated successfully as ([^\s,]+)", content)
                if match:
                    return match.group(1)
        except Exception as e:
            logger.warning(f"Failed to extract email from agy log: {e}")
    return "Аккаунт активен"


def get_auth_state_signature() -> str:
    """Compute a signature representing current agy CLI authentication state.

    Checks token, settings, state files in ~/.gemini/antigravity-cli/.
    Returns string signature (mtime + hash) to detect hot-reload account switches.
    """
    home = Path.home()
    base_dir = home / ".gemini" / "antigravity-cli"
    token_file = base_dir / "antigravity-oauth-token"
    settings_file = base_dir / "settings.json"
    jetski_file = base_dir / "jetski_state.pbtxt"
    
    # Also check latest log file mtime
    log_dir = base_dir / "log"
    latest_log = None
    if log_dir.exists():
        logs = sorted(log_dir.glob("cli-*.log"), key=lambda f: f.stat().st_mtime, reverse=True)
        if logs:
            latest_log = logs[0]

    parts = []
    for fpath in (token_file, settings_file, jetski_file, latest_log):
        if fpath and fpath.exists():
            try:
                st = fpath.stat()
                content = fpath.read_bytes()[:1024]
                h = hashlib.md5(content).hexdigest()[:8]
                parts.append(f"{fpath.name}:{st.st_mtime}:{st.st_size}:{h}")
            except Exception:
                parts.append(f"{fpath.name}:err")
        else:
            name = fpath.name if fpath else "none"
            parts.append(f"{name}:missing")

    return "|".join(parts)


class AgySession:
    """Manages an interactive PTY session for a single chat with model, effort, and mode controls."""
    def __init__(self, chat_id: int, model_name: str = "gemini-3.1-pro-high", effort: str = "high", mode: str = "default", conversation_id: Optional[str] = None):
        self.chat_id = chat_id
        self.child = None
        self.model_name = model_name
        self.effort = effort
        self.mode = mode
        self.conversation_id = conversation_id
        self.spawn_auth_signature = None
        self._lock = asyncio.Lock()

    def set_model(self, model_key: str) -> bool:
        if model_key in AVAILABLE_MODELS:
            new_model = AVAILABLE_MODELS[model_key]
        elif model_key in AVAILABLE_MODELS.values():
            new_model = model_key
        else:
            return False

        if self.model_name != new_model:
            self.model_name = new_model
            logger.info(f"Switching model for chat_id={self.chat_id} to {self.model_name}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id)
        return True

    def set_effort(self, effort_level: str) -> bool:
        if effort_level in AVAILABLE_EFFORTS:
            if self.effort != effort_level:
                self.effort = effort_level
                logger.info(f"Switching effort for chat_id={self.chat_id} to {self.effort}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id)
            return True
        return False

    def set_mode(self, mode_key: str) -> bool:
        if mode_key in AVAILABLE_MODES:
            if self.mode != mode_key:
                self.mode = mode_key
                logger.info(f"Switching mode for chat_id={self.chat_id} to {self.mode}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id)
            return True
        return False

    def set_conversation(self, conversation_id: Optional[str]) -> bool:
        """Switch or resume a specific agy conversation by ID or 'latest'."""
        if self.conversation_id != conversation_id:
            self.conversation_id = conversation_id
            logger.info(f"Switching conversation for chat_id={self.chat_id} to {conversation_id}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id)
        return True

    async def _ensure_started(self):
        """Spawns process with configured flags and MCP environment bindings.
        
        Hot-reloads PTY process if host agy CLI credentials or account changed.
        """
        current_auth_sig = get_auth_state_signature()

        # Hot reload check: if process is running but auth credentials/account changed on host
        if self.child and self.child.isalive():
            if self.spawn_auth_signature and self.spawn_auth_signature != current_auth_sig:
                logger.info(
                    f"⚡ HOT RELOAD DETECTED: Account credentials changed on host server! "
                    f"Terminating old PTY session for chat_id={self.chat_id} to reload new account credentials."
                )
                self.close()

        if not self.child or not self.child.isalive():
            args = [
                "--model", self.model_name,
                "--effort", self.effort,
                "--dangerously-skip-permissions"
            ]
            if self.mode != "default":
                args.extend(["--mode", self.mode])
            
            # Attach --conversation or --continue flag to resume specific conversation thread
            if self.conversation_id == "latest":
                args.append("--continue")
            elif self.conversation_id:
                args.extend(["--conversation", self.conversation_id])

            logger.info(f"Spawning agy PTY process for chat_id={self.chat_id} args={args}")
            env = os.environ.copy()
            env["TERM"] = "xterm"

            # Inject active MCP server environment variables into agy CLI process
            servers = mcp_config.config.get("servers", {})
            if servers.get("searxng", {}).get("enabled"):
                env["SEARXNG_URL"] = servers["searxng"].get("url", "http://127.0.0.1:8889")
            if servers.get("anythingllm", {}).get("enabled"):
                env["ANYTHINGLLM_URL"] = servers["anythingllm"].get("url", "http://127.0.0.1:3002")
                env["ANYTHINGLLM_API_KEY"] = servers["anythingllm"].get("api_key", "")
            if servers.get("nextcloud", {}).get("enabled"):
                env["NEXTCLOUD_URL"] = servers["nextcloud"].get("url", "http://127.0.0.1:8000")

            self.child = pexpect.spawn(
                AGY_BINARY_PATH,
                args,
                env=env,
                echo=False,
                timeout=300
            )
            self.spawn_auth_signature = current_auth_sig

            # Drain startup banner
            idle_count = 0
            while idle_count < 3:
                try:
                    await asyncio.to_thread(self.child.read_nonblocking, size=1024, timeout=0.5)
                    idle_count = 0
                except pexpect.TIMEOUT:
                    idle_count += 1
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    break

    async def get_response(self, prompt: str) -> str:
        """Sends prompt to agy, uses pyte Virtual Terminal to render clean screen output."""
        async with self._lock:
            await self._ensure_started()

            clean_prompt = prompt.replace("\n", " ").strip()
            try:
                self.child.send((clean_prompt + "\r\n").encode("utf-8"))
            except (pexpect.EOF, pexpect.ExceptionPexpect, OSError) as e:
                logger.warning(f"Failed to send prompt to agy process for chat_id={self.chat_id}: {e}")
                self.close()
                await self._ensure_started()
                self.child.send((clean_prompt + "\r\n").encode("utf-8"))

            screen = pyte.Screen(120, 60)
            stream = pyte.ByteStream(screen)

            idle_count = 0
            received_bytes = False

            while True:
                try:
                    chunk = await asyncio.to_thread(
                        self.child.read_nonblocking, size=1024, timeout=0.5
                    )
                    if chunk:
                        received_bytes = True
                        stream.feed(chunk)
                        idle_count = 0
                except pexpect.TIMEOUT:
                    if received_bytes:
                        idle_count += 1
                        if idle_count >= 12:
                            break
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    break

            lines = list(screen.display)
            formatted_response = format_dyslexia_friendly_text(lines)
            return formatted_response

    def close(self):
        """Terminates active agy process cleanly and forcefully."""
        child = self.child
        self.child = None
        if child:
            try:
                pid = getattr(child, "pid", None)
                if hasattr(child, "isalive") and callable(child.isalive) and child.isalive():
                    child.close(force=True)
                if pid and isinstance(pid, int):
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except OSError:
                        pass
            except Exception as e:
                logger.warning(f"Error closing agy session for chat_id={self.chat_id}: {e}")

