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
from src.formatters import format_dyslexia_friendly_text, extract_new_response_lines, is_tui_noise

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


def _get_gemini_dir() -> Path:
    """Find the active .gemini/antigravity-cli directory across possible HOME paths."""
    homes_to_check = [Path.home()]
    sudo_user = os.getenv("SUDO_USER")
    if sudo_user:
        homes_to_check.append(Path(f"/home/{sudo_user}"))
    
    if Path("/root").exists():
        homes_to_check.append(Path("/root"))
    if Path("/home").exists():
        for p in Path("/home").iterdir():
            if p.is_dir():
                homes_to_check.append(p)

    for h in homes_to_check:
        target = h / ".gemini" / "antigravity-cli"
        try:
            if (target / "antigravity-oauth-token").exists():
                return target
        except PermissionError:
            pass
    return Path.home() / ".gemini" / "antigravity-cli"


def get_active_account_email() -> str:
    """Retrieve the currently authenticated Google account email via OAuth, JWT payload, or CLI logs."""
    base_dir = _get_gemini_dir()
    token_file = base_dir / "antigravity-oauth-token"
    
    if token_file.exists():
        try:
            data = json.loads(token_file.read_text(encoding="utf-8"))
            token_dict = data.get("token", {}) if isinstance(data, dict) else {}
            
            access_token = token_dict.get("access_token")
            if access_token:
                try:
                    req = urllib.request.Request("https://www.googleapis.com/oauth2/v3/userinfo")
                    req.add_header("Authorization", f"Bearer {access_token}")
                    with urllib.request.urlopen(req, timeout=3.0) as resp:
                        info = json.loads(resp.read().decode("utf-8"))
                        email = info.get("email")
                        if email:
                            return email
                except Exception as net_err:
                    logger.debug(f"Userinfo API call failed: {net_err}")

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
            logger.debug(f"Error reading antigravity-oauth-token: {e}")

    settings_file = base_dir / "settings.json"
    if settings_file.exists():
        try:
            settings_data = json.loads(settings_file.read_text(encoding="utf-8"))
            email = settings_data.get("email") or settings_data.get("user", {}).get("email")
            if email:
                return email
        except Exception:
            pass

    return ""


def get_auth_state_signature() -> str:
    """Calculate a hash signature of active auth tokens and account configs."""
    base_dir = _get_gemini_dir()
    sig_parts = []
    
    token_file = base_dir / "antigravity-oauth-token"
    if token_file.exists():
        try:
            st = token_file.stat()
            content = token_file.read_bytes()
            h = hashlib.sha256(content).hexdigest()
            sig_parts.append(f"token:{st.st_mtime}:{h}")
        except Exception:
            pass
            
    settings_file = base_dir / "settings.json"
    if settings_file.exists():
        try:
            st = settings_file.stat()
            content = settings_file.read_bytes()
            h = hashlib.sha256(content).hexdigest()
            sig_parts.append(f"settings:{st.st_mtime}:{h}")
        except Exception:
            pass

    return "|".join(sig_parts) if sig_parts else "none"


class AgySession:
    """Manages an active PTY session to the agy command-line agent."""

    def __init__(
        self,
        chat_id: int,
        model_name: str = "gemini-3.6-flash-low",
        effort: str = "low",
        mode: str = "default",
        conversation_id: Optional[str] = None,
        workspace: Optional[str] = None
    ):
        self.chat_id = chat_id
        self.child = None
        self.model_name = model_name
        self.effort = effort
        self.mode = mode
        self.conversation_id: Optional[str] = conversation_id
        self.workspace: Optional[str] = workspace
        self.spawn_auth_signature: Optional[str] = None
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
            if self.child and self.child.isalive():
                self.child.sendline(f"/model {self.model_name}")
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_effort(self, effort_level: str) -> bool:
        if effort_level in AVAILABLE_EFFORTS:
            if self.effort != effort_level:
                self.effort = effort_level
                logger.info(f"Switching effort for chat_id={self.chat_id} to {self.effort}")
                if self.child and self.child.isalive():
                    self.child.sendline(f"/effort {self.effort}")
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
        if self.conversation_id != conversation_id:
            self.conversation_id = conversation_id
            logger.info(f"Switching conversation for chat_id={self.chat_id} to {conversation_id}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_workspace(self, workspace: Optional[str]) -> bool:
        if self.workspace != workspace:
            self.workspace = workspace
            logger.info(f"Switching workspace for chat_id={self.chat_id} to {workspace}")
            self.close()
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def _detect_conversation_id(self):
        """Detect conversation ID created by THIS bot's agy child process via directory delta.

        For 'latest' mode (--continue): resolves to actual UUID via agy CLI database,
        since --continue reuses an existing brain directory and won't create a new one.
        For new sessions (conversation_id=None): uses brain directory delta detection.
        """
        if self.conversation_id and self.conversation_id != "latest":
            return

        # Strategy 1: For "latest", resolve via agy CLI conversation_summaries.db
        if self.conversation_id == "latest":
            try:
                from src.conversations import get_latest_conversation_id
                resolved = get_latest_conversation_id()
                if resolved:
                    logger.info(f"Resolved 'latest' conversation_id to {resolved} for chat_id={self.chat_id}")
                    self.conversation_id = resolved
                    save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
                    return
            except Exception as e:
                logger.warning(f"Failed to resolve 'latest' conversation_id for chat_id={self.chat_id}: {e}")

        # Strategy 2: Delta-detection for brand new sessions (conversation_id=None)
        brain_base = Path.home() / ".gemini" / "antigravity-cli" / "brain"
        if not brain_base.exists():
            return

        try:
            uuid_pattern = re.compile(r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
            known = getattr(self, "_spawn_brain_dirs", set())
            current = set(d.name for d in brain_base.iterdir() if d.is_dir() and uuid_pattern.match(d.name))
            
            new_dirs = list(current - known)
            if new_dirs:
                # Pick the newest directory from new_dirs
                best_id = max(new_dirs, key=lambda name: (brain_base / name).stat().st_mtime)
                if best_id != self.conversation_id:
                    old_id = self.conversation_id
                    self.conversation_id = best_id
                    logger.info(f"Detected newly spawned conversation_id={best_id} for chat_id={self.chat_id} (was: {old_id})")
                    save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        except Exception as e:
            logger.warning(f"Failed to detect conversation_id for chat_id={self.chat_id}: {e}")

    async def _ensure_started(self):
        current_auth_sig = get_auth_state_signature()

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
            if self.conversation_id:
                if self.conversation_id == "latest":
                    args.append("--continue")
                else:
                    args.extend(["--conversation", self.conversation_id])

            logger.info(f"Spawning agy PTY process for chat_id={self.chat_id} args={args} cwd={self.workspace}")
            
            env = os.environ.copy()
            env["TERM"] = "xterm-256color"
            env["LANG"] = "en_US.UTF-8"
            env["LC_ALL"] = "en_US.UTF-8"
            env["PYTHONIOENCODING"] = "utf-8"
            
            mcp_env = mcp_config.get_env_dict()
            env.update(mcp_env)

            brain_base = Path.home() / ".gemini" / "antigravity-cli" / "brain"
            if brain_base.exists():
                self._spawn_brain_dirs = set(d.name for d in brain_base.iterdir() if d.is_dir())
            else:
                self._spawn_brain_dirs = set()

            self.child = pexpect.spawn(
                AGY_BINARY_PATH,
                args,
                encoding=None,
                cwd=self.workspace,
                env=env,
                echo=False,
                timeout=300
            )
            self.child.setwinsize(3000, 120)
            self.spawn_auth_signature = current_auth_sig

            screen = pyte.Screen(120, 3000)
            stream = pyte.ByteStream(screen)
            idle_count = 0
            menu_confirmed = False
            # MCP servers can take a while to initialize, wait up to 60 seconds (120 * 0.5s)
            while idle_count < 120:
                if self.child is None or not self.child.isalive():
                    logger.error("CLI process died during startup")
                    break
                try:
                    chunk = await asyncio.to_thread(self.child.read_nonblocking, size=1024, timeout=0.5)
                    if chunk:
                        stream.feed(chunk)
                        idle_count = 0
                        
                        if not menu_confirmed:
                            banner_text = "\n".join(_safe_screen_display(screen)).lower()
                            
                            # Abort if auth requires interactive login, otherwise we hang on OAuth
                            if "select login method" in banner_text:
                                logger.error(f"Auth lost detected for chat_id={self.chat_id}. Aborting.")
                                self.close()
                                raise RuntimeError("⚠️ <b>Agent lost authorization!</b>\nPlease log in to the server via SSH as the root user and execute the <code>agy auth login</code> command, then repeat your request.")

                            if any(phrase in banner_text for phrase in ["arrow keys to navigate", "use arrow keys", "what would you like to do"]):
                                logger.info("Auto-confirming initial agy CLI interactive prompt with Enter")
                                self.child.send(b"\r\n")
                                menu_confirmed = True
                                screen.reset()
                                continue  # Skip prompt check on the exact tick we confirm the menu

                        # Wait for the actual prompt to appear
                        raw_lines = _safe_screen_display(screen)
                        for l in reversed(raw_lines):
                            clean_l = l.strip()
                            clean_l_no_ansi = re.sub(r'\x1b\[.*?m', '', clean_l)
                            if not clean_l_no_ansi:
                                continue
                            if (clean_l_no_ansi.startswith("? ") and "for shortcuts" in clean_l_no_ansi) or "───" in clean_l_no_ansi:
                                continue
                            if clean_l_no_ansi in (">", "❯", "›") or clean_l_no_ansi.startswith("> ") or clean_l_no_ansi.startswith("❯ ") or clean_l_no_ansi.startswith("› ") or clean_l_no_ansi.startswith("? "):
                                logger.info("Ready prompt detected after cold start.")
                                return
                            break
                    else:
                        break
                    await asyncio.sleep(0.01)
                except pexpect.TIMEOUT:
                    idle_count += 1
                    await asyncio.sleep(0.05)
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    break
            
            # If we exited the loop without returning, we timed out or hit EOF
            logger.error(f"Failed to detect ready prompt for chat_id={self.chat_id}. PTY might be hung.")
            self.close()
            raise RuntimeError("⚠️ <b>Startup Error:</b> The CLI process could not start correctly or hung. Please try to reset the session via <code>/new</code>.")

    async def stream_response(self, prompt: str):
        """Yields progressive formatted text chunks as agy generates content on screen."""
        async with self._lock:
            await self._ensure_started()

            if not self.child or not self.child.isalive():
                yield "⚠️ Failed to launch CLI process. Try /new and repeat the request."
                return

            clean_prompt = prompt.replace("\n", " ").strip()
            try:
                self.child.send((clean_prompt + "\n").encode("utf-8"))
            except (pexpect.EOF, pexpect.ExceptionPexpect, OSError) as e:
                logger.warning(f"Failed to send prompt to agy process for chat_id={self.chat_id}: {e}")
                self.close()
                await self._ensure_started()
                self.child.send((clean_prompt + "\n").encode("utf-8"))

            screen = pyte.Screen(120, 3000)
            stream = pyte.ByteStream(screen)

            total_timeout_count = 0
            received_content_bytes = False
            last_content_hash = None
            content_stable_ticks = 0
            last_yielded_text = ""

            while True:
                if self.child is None or not self.child.isalive():
                    yield "\n\n⚠️ *Process interrupted or configuration changed. Request cancelled.*"
                    break
                try:
                    chunk = await asyncio.to_thread(
                        self.child.read_nonblocking, size=4096, timeout=0.1
                    )
                    if chunk:
                        stream.feed(chunk)
                except pexpect.TIMEOUT:
                    total_timeout_count += 1

                    # Check content stability & stream updates every 10 ticks (~1s)
                    if total_timeout_count % 10 == 0:
                        raw_lines = _safe_screen_display(screen)
                        new_lines = extract_new_response_lines(raw_lines, prompt=prompt)
                        has_content = any(l.strip() and not is_tui_noise(l, prompt) for l in new_lines)

                        if has_content:
                            received_content_bytes = True
                            formatted = format_dyslexia_friendly_text(list(raw_lines), prompt=prompt)
                            if formatted.strip() and formatted != last_yielded_text:
                                last_yielded_text = formatted
                                yield formatted

                        content_lines = [l for l in raw_lines if l.strip()]
                        content_hash = hash(tuple(content_lines))

                        if content_hash == last_content_hash:
                            content_stable_ticks += 1
                            
                            is_prompt_ready = False
                            for l in reversed(raw_lines):
                                l_str = l.strip()
                                if l_str:
                                    clean_l = re.sub(r'\x1b\[.*?m', '', l_str)
                                    if (clean_l.startswith("? ") and "for shortcuts" in clean_l) or "───" in clean_l:
                                        continue
                                    if clean_l in (">", "❯", "›") or clean_l.startswith("> ") or clean_l.startswith("❯ ") or clean_l.startswith("› ") or clean_l.startswith("? "):
                                        is_prompt_ready = True
                                    break
                                    
                            if is_prompt_ready and content_stable_ticks >= 15 and received_content_bytes:
                                break
                            elif content_stable_ticks >= 600 and not received_content_bytes:
                                logger.warning(f"No response timeout (60s) for chat_id={self.chat_id}")
                                break
                            elif content_stable_ticks >= 600:  # 60 seconds fallback for long tool calls
                                logger.warning(f"Timeout fallback triggered for chat_id={self.chat_id} (60s stable without prompt)")
                                break
                        else:
                            content_stable_ticks = 0
                        last_content_hash = content_hash

                    # Hard timeout: 1800 seconds max (30 min)
                    if total_timeout_count >= 18000:
                        logger.warning(f"Max timeout reached (1800s) for chat_id={self.chat_id}")
                        break

                    # No content at all after 900 seconds = agy failed to respond or is stuck loading huge context
                    if not received_content_bytes and total_timeout_count >= 9000:
                        logger.warning(f"CLI timeout for chat_id={self.chat_id} (no response after 900s)")
                        break

                    await asyncio.sleep(0.05)
                except (pexpect.EOF, pexpect.ExceptionPexpect, OSError):
                    raw_lines = _safe_screen_display(screen)
                    final_text = "\n".join(l.strip() for l in raw_lines if l.strip())
                    if final_text and not received_content_bytes:
                        clean = re.sub(r'\x1b\[.*?m', '', final_text)
                        yield clean
                        received_content_bytes = True
                    break

            lines = list(_safe_screen_display(screen))
            final_formatted = format_dyslexia_friendly_text(lines, prompt=prompt)
            if not final_formatted.strip():
                logger.warning(f"Empty or thinking-suppressed response detected from model {self.model_name} for chat_id={self.chat_id}")

            self._detect_conversation_id()
            yield final_formatted

    async def get_response(self, prompt: str) -> str:
        """Sends prompt to agy and returns final rendered response."""
        res = ""
        async for chunk in self.stream_response(prompt):
            res = chunk
        return res

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

            for _ in range(4):
                screen = pyte.Screen(120, 3000)
                stream = pyte.ByteStream(screen)
                idle_count = 0
                while idle_count < 3:
                    try:
                        chunk = await asyncio.to_thread(self.child.read_nonblocking, size=4096, timeout=0.3)
                        if chunk:
                            stream.feed(chunk)
                            idle_count = 0
                        else:
                            break
                        await asyncio.sleep(0.01)
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

            try:
                self.child.send(b"\x1b")
                await asyncio.sleep(0.3)
            except Exception:
                pass

            email = get_active_account_email()
            return format_usage_response(all_lines, email)

    def clear_context(self):
        """Send the /clear command to the active PTY to reset the conversation without killing the process."""
        self.conversation_id = None
        if self.child and self.child.isalive():
            try:
                self.child.sendline("/clear")
                logger.info(f"Sent /clear to PTY for chat_id={self.chat_id}")
            except Exception as e:
                logger.error(f"Failed to send /clear: {e}")
                
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
                        # Check if process is still agy to avoid PID recycling bug
                        with open(f"/proc/{pid}/comm", "r") as f:
                            comm = f.read().strip()
                        if "agy" in comm or "python" in comm:
                            os.kill(pid, signal.SIGTERM)
                            import time; time.sleep(0.1)
                            os.kill(pid, signal.SIGKILL)
                    except OSError:
                        pass
            except Exception as e:
                logger.warning(f"Error closing agy session for chat_id={self.chat_id}: {e}")


CLIRunnerSession = AgySession

