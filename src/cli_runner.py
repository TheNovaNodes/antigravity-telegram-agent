import asyncio
import logging
import os
import pexpect
import pyte
from src.config import AGY_BINARY_PATH
from src.db import save_user_session

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

class AgySession:
    """Manages an interactive PTY session for a single chat with model, effort, and mode controls."""
    def __init__(self, chat_id: int, model_name: str = "gemini-3.1-pro-high", effort: str = "high", mode: str = "default"):
        self.chat_id = chat_id
        self.child = None
        self.model_name = model_name
        self.effort = effort
        self.mode = mode
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
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode)
        return True

    def set_effort(self, effort_level: str) -> bool:
        if effort_level in AVAILABLE_EFFORTS:
            if self.effort != effort_level:
                self.effort = effort_level
                logger.info(f"Switching effort for chat_id={self.chat_id} to {self.effort}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode)
            return True
        return False

    def set_mode(self, mode_key: str) -> bool:
        if mode_key in AVAILABLE_MODES:
            if self.mode != mode_key:
                self.mode = mode_key
                logger.info(f"Switching mode for chat_id={self.chat_id} to {self.mode}")
                self.close()
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode)
            return True
        return False

    async def _ensure_started(self):
        """Spawns process with configured flags and drains startup banner."""
        if not self.child or not self.child.isalive():
            args = [
                "--model", self.model_name,
                "--effort", self.effort,
                "--dangerously-skip-permissions"
            ]
            if self.mode != "default":
                args.extend(["--mode", self.mode])

            logger.info(f"Spawning agy PTY process for chat_id={self.chat_id} args={args}")
            env = os.environ.copy()
            env["TERM"] = "xterm"
            self.child = pexpect.spawn(
                AGY_BINARY_PATH,
                args,
                env=env,
                echo=False,
                timeout=300
            )
            # Drain startup banner
            idle_count = 0
            while idle_count < 3:
                try:
                    await asyncio.to_thread(self.child.read_nonblocking, size=1024, timeout=0.5)
                    idle_count = 0
                except pexpect.TIMEOUT:
                    idle_count += 1
                except pexpect.EOF:
                    break

    async def get_response(self, prompt: str) -> str:
        """Sends prompt to agy, uses pyte Virtual Terminal to render clean screen output."""
        async with self._lock:
            await self._ensure_started()

            clean_prompt = prompt.replace("\n", " ").strip()
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
                        stream.feed(chunk)
                        received_bytes = True
                        idle_count = 0
                except pexpect.TIMEOUT:
                    idle_count += 1
                    if received_bytes and idle_count >= 6:
                        break
                    if idle_count >= 80:
                        return "⚠️ [Таймаут ответа от агента]"
                except pexpect.EOF:
                    logger.warning(f"Session for chat_id={self.chat_id} reached EOF.")
                    break

            clean_lines = []
            for line in screen.display:
                l = line.rstrip()
                if not l.strip():
                    continue
                if "────" in l or "esc to cancel" in l or "Generating..." in l or "Antigravity CLI" in l:
                    continue
                if l.strip().startswith(">") or l.strip().startswith("Gemini") or l.strip().startswith("Claude") or l.strip().startswith("GPT-OSS"):
                    continue
                
                # Strip leading TUI margins
                if l.startswith("    "):
                    l = l[4:]
                elif l.startswith("   "):
                    l = l[3:]
                elif l.startswith("  "):
                    l = l[2:]

                clean_lines.append(l)

            return "\n".join(clean_lines).strip()

    def close(self):
        if self.child and self.child.isalive():
            try:
                self.child.close(force=True)
            except Exception as e:
                logger.error(f"Error closing session for chat_id={self.chat_id}: {e}")
            logger.info(f"Closed agy session for chat_id={self.chat_id}")
