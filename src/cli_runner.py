import asyncio
import base64
import hashlib
import json
import logging
import os
import re
import signal
import urllib.request
from pathlib import Path
from typing import Optional

import pexpect
import pyte
from src.config import AGY_BINARY_PATH
from src.db import save_user_session
from src.mcp_config import mcp_config
from src.formatters import format_dyslexia_friendly_text

logger = logging.getLogger(__name__)

def _safe_screen_display(screen):
    try:
        return screen.display
    except IndexError:
        for y in range(screen.lines):
            for x in range(screen.columns):
                char = screen.buffer[y][x]
                if not char.data:
                    screen.buffer[y][x] = char._replace(data=" ")
        return screen.display

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

def _get_gemini_dir() -> Path:
    """Find the active .gemini/antigravity-cli directory across possible HOME paths."""
    homes_to_check = [Path.home()]
    # Check SUDO_USER home if present
    sudo_user = os.getenv("SUDO_USER")
    if sudo_user:
        homes_to_check.append(Path(f"/home/{sudo_user}"))
    
    # Also scan /home/* and /root
    if Path("/root").exists():
        homes_to_check.append(Path("/root"))
    if Path("/home").exists():
        for p in Path("/home").iterdir():
            if p.is_dir():
                homes_to_check.append(p)

    for h in homes_to_check:
        target = h / ".gemini" / "antigravity-cli"
        if (target / "antigravity-oauth-token").exists():
            return target
    return Path.home() / ".gemini" / "antigravity-cli"


def get_active_account_email() -> str:
    """Retrieve the currently authenticated Google account email via OAuth, JWT payload, or CLI logs."""
    base_dir = _get_gemini_dir()
    token_file = base_dir / "antigravity-oauth-token"
    
    # 1. Try OAuth token file
    if token_file.exists():
        try:
            data = json.loads(token_file.read_text())
            token_dict = data.get("token", {}) if isinstance(data, dict) else {}
            
            # 1a. Try Google OAuth userinfo API endpoint
            access_token = token_dict.get("access_token")
            if access_token:
                try:
                    req = urllib.request.Request("https://www.googleapis.com/oauth2/v3/userinfo")
                    req.add_header("Authorization", f"Bearer {access_token}")
                    with urllib.request.urlopen(req, timeout=3.0) as resp:
                        info = json.loads(resp.read().decode())
                        email = info.get("email")
                        if email:
                            return email
                except Exception as net_err:
                    logger.debug(f"Userinfo API call failed: {net_err}")

            # 1b. Try decoding JWT id_token (doesn't expire or depend on network)
            id_token = token_dict.get("id_token") or data.get("id_token")
            if id_token:
                try:
                    parts = id_token.split(".")
                    if len(parts) >= 2:
                        payload_b64 = parts[1]
                        rem = len(payload_b64) % 4
                        if rem > 0:
                            payload_b64 += "=" * (4 - rem)
                        jwt_data = json.loads(base64.urlsafe_b64decode(payload_b64).decode("utf-8"))
                        email = jwt_data.get("email")
                        if email:
                            return email
                except Exception as jwt_err:
                    logger.debug(f"Failed to decode id_token JWT: {jwt_err}")
        except Exception as e:
            logger.warning(f"Failed to parse token file: {e}")

    # 2. Robust multi-file log scanner (cli.log and log/cli-*.log)
    log_files = []
    cli_log = base_dir / "cli.log"
    if cli_log.exists():
        log_files.append(cli_log)

    log_dir = base_dir / "log"
    if log_dir.exists():
        log_files.extend(sorted(log_dir.glob("cli-*.log"), key=lambda f: f.stat().st_mtime, reverse=True))

    for lf in log_files:
        try:
            content = lf.read_text(errors="ignore")
            # Explicit login success patterns
            match = re.search(
                r"(?:authenticated successfully as|logged in as|user:|account:)\s*([a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,})",
                content,
                re.IGNORECASE
            )
            if match:
                return match.group(1)

            # General email pattern matching
            emails = re.findall(r"[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}", content)
            for email in reversed(emails):
                lower_e = email.lower()
                if not any(domain in lower_e for domain in ["example.com", "google.com", "schema.org", "googleapis.com"]):
                    return email
        except Exception:
            pass

    return "Аккаунт активен"


def get_auth_state_signature() -> str:
    """Compute a signature representing current agy CLI authentication state.

    Checks token, settings, state files in ~/.gemini/antigravity-cli/.
    Returns string signature (mtime + hash) to detect hot-reload account switches.
    """
    base_dir = _get_gemini_dir()
    token_file = base_dir / "antigravity-oauth-token"
    settings_file = base_dir / "settings.json"
    jetski_file = base_dir / "jetski_state.pbtxt"
    # NOTE: cli.log and log/cli-*.log are intentionally EXCLUDED from signature.
    # Including them caused a HOT RELOAD storm: agy writes to these logs on every
    # spawn, mutating the signature, which triggers another reload — infinite loop.
    # Only credential/config files that change on actual account switch are monitored.
    
    parts = []
    for fpath in (token_file, settings_file, jetski_file):
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
    def __init__(self, chat_id: int, model_name: str = "gemini-3.1-pro-high", effort: str = "high", mode: str = "default", conversation_id: Optional[str] = None, workspace: Optional[str] = None):
        self.chat_id = chat_id
        self.child = None
        self.model_name = model_name
        self.effort = effort
        self.mode = mode
        self.conversation_id = conversation_id
        self.workspace = workspace
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
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_effort(self, effort_level: str) -> bool:
        if effort_level in AVAILABLE_EFFORTS:
            if self.effort != effort_level:
                self.effort = effort_level
                logger.info(f"Switching effort for chat_id={self.chat_id} to {self.effort}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
            return True
        return False

    def set_mode(self, mode_key: str) -> bool:
        if mode_key in AVAILABLE_MODES:
            if self.mode != mode_key:
                self.mode = mode_key
                logger.info(f"Switching mode for chat_id={self.chat_id} to {self.mode}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
            return True
        return False

    def set_conversation(self, conversation_id: Optional[str]) -> bool:
        """Switch or resume a specific agy conversation by ID or 'latest'."""
        if self.conversation_id != conversation_id:
            self.conversation_id = conversation_id
            logger.info(f"Switching conversation for chat_id={self.chat_id} to {conversation_id}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_workspace(self, workspace: Optional[str]) -> bool:
        """Switch workspace directory for the session."""
        if self.workspace != workspace:
            self.workspace = workspace
            logger.info(f"Switching workspace for chat_id={self.chat_id} to {workspace}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def _detect_conversation_id(self):
        """Detect active conversation_id by finding the most recently modified brain directory.
        
        Scans ~/.gemini/antigravity-cli/brain/ for UUID-named directories and picks
        the one with the newest mtime. Updates self.conversation_id and persists to DB
        if changed. This enables artifact delivery to find the correct brain directory.
        """
        brain_base = Path.home() / ".gemini" / "antigravity-cli" / "brain"
        if not brain_base.exists():
            return
            
        # Only detect if we don't already have a specific UUID
        if self.conversation_id and self.conversation_id != "latest":
            return

        try:
            uuid_pattern = re.compile(r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
            best_id = None
            best_mtime = 0

            for d in brain_base.iterdir():
                if d.is_dir() and uuid_pattern.match(d.name):
                    try:
                        mtime = d.stat().st_mtime
                        transcript_path = d / ".system_generated" / "logs" / "transcript.jsonl"
                        if transcript_path.exists():
                            mtime = max(mtime, transcript_path.stat().st_mtime)
                            
                        if mtime > best_mtime:
                            best_mtime = mtime
                            best_id = d.name
                    except Exception:
                        pass

            if best_id and best_id != self.conversation_id:
                old_id = self.conversation_id
                self.conversation_id = best_id
                logger.info(f"Detected conversation_id={best_id} for chat_id={self.chat_id} (was: {old_id})")
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        except Exception as e:
            logger.warning(f"Failed to detect conversation_id for chat_id={self.chat_id}: {e}")

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
            # If no conversation_id is set, do not pass --continue.
            # This ensures isolated sessions for new chats instead of grabbing the global latest.

            logger.info(f"Spawning agy PTY process for chat_id={self.chat_id} args={args} cwd={self.workspace}")
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
                cwd=self.workspace,
                env=env,
                echo=False,
                timeout=300
            )
            self.child.setwinsize(6000, 120)
            self.spawn_auth_signature = current_auth_sig

            # Drain startup banner and auto-confirm first-run interactive prompts
            screen = pyte.Screen(120, 6000)
            stream = pyte.ByteStream(screen)
            idle_count = 0
            while idle_count < 3:
                try:
                    chunk = await asyncio.to_thread(self.child.read_nonblocking, size=1024, timeout=0.5)
                    if chunk:
                        stream.feed(chunk)
                        idle_count = 0
                        banner_text = "\n".join(_safe_screen_display(screen)).lower()
                        if any(phrase in banner_text for phrase in ["arrow keys to navigate", "enter to select", "press enter"]):
                            logger.info("Auto-confirming initial agy CLI interactive prompt with Enter")
                            self.child.send(b"\r\n")
                            screen.reset()
                except pexpect.TIMEOUT:
                    idle_count += 1
                    await asyncio.sleep(0.05)
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    break

    async def get_response(self, prompt: str) -> str:
        """Sends prompt to agy, uses pyte Virtual Terminal to render clean screen output."""
        async with self._lock:
            await self._ensure_started()

            if not self.child or not self.child.isalive():
                return "⚠️ Не удалось запустить CLI-процесс. Попробуйте /reset и повторите запрос."

            clean_prompt = prompt.replace("\n", " ").strip()
            try:
                self.child.send((clean_prompt + "\r\n").encode("utf-8"))
            except (pexpect.EOF, pexpect.ExceptionPexpect, OSError) as e:
                logger.warning(f"Failed to send prompt to agy process for chat_id={self.chat_id}: {e}")
                self.close()
                await self._ensure_started()
                self.child.send((clean_prompt + "\r\n").encode("utf-8"))

            screen = pyte.Screen(120, 6000)
            stream = pyte.ByteStream(screen)

            idle_count = 0
            total_timeout_count = 0
            received_bytes = False

            while True:
                try:
                    chunk = await asyncio.to_thread(
                        self.child.read_nonblocking, size=4096, timeout=0.1
                    )
                    if chunk:
                        received_bytes = True
                        stream.feed(chunk)
                        idle_count = 0
                except pexpect.TIMEOUT:
                    total_timeout_count += 1
                    if received_bytes:
                        idle_count += 1
                        if idle_count >= 40:  # ~4 seconds of silence after stream output
                            break
                    elif total_timeout_count >= 300:  # 30 seconds max timeout if CLI hangs
                        logger.warning(f"CLI timeout for chat_id={self.chat_id} (no response after 30s)")
                        break
                    await asyncio.sleep(0.05)
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    break

            lines = list(_safe_screen_display(screen))
            formatted_response = format_dyslexia_friendly_text(lines, prompt=prompt)
            if not formatted_response.strip():
                logger.warning(f"Empty or thinking-suppressed response detected from model {self.model_name} for chat_id={self.chat_id}")

            # Detect and track the active conversation_id from brain directory
            self._detect_conversation_id()

            return formatted_response

    async def get_usage_info(self) -> str:
        """Sends /usage to agy, scrolls modal overlay to capture all model quotas, and closes modal cleanly."""
        from src.formatters import format_usage_response
        async with self._lock:
            await self._ensure_started()

            try:
                self.child.send(b"/usage\r\n")
            except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                self.close()
                await self._ensure_started()
                self.child.send(b"/usage\r\n")

            all_lines = []

            # Page down 4 times to capture all models across modal pages
            for _ in range(4):
                screen = pyte.Screen(120, 6000)
                stream = pyte.ByteStream(screen)
                idle_count = 0
                while idle_count < 3:
                    try:
                        chunk = await asyncio.to_thread(self.child.read_nonblocking, size=4096, timeout=0.3)
                        if chunk:
                            stream.feed(chunk)
                            idle_count = 0
                    except (pexpect.TIMEOUT, pexpect.EOF, OSError):
                        idle_count += 1

                for line in _safe_screen_display(screen):
                    s = line.strip()
                    if s and s not in all_lines:
                        all_lines.append(s)

                try:
                    self.child.send(b"\x1b[6~")
                except Exception:
                    pass
                await asyncio.sleep(0.3)

            # Close modal overlay using Escape
            try:
                self.child.send(b"\x1b")
                await asyncio.sleep(0.3)
            except Exception:
                pass

            email = get_active_account_email()
            return format_usage_response(all_lines, email)

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

