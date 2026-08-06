import asyncio
import logging
import re
import pexpect
from src.config import AGY_BINARY_PATH

logger = logging.getLogger(__name__)
ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')

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

class AgySession:
    """Manages an interactive PTY session for a single chat with model switching."""
    def __init__(self, chat_id: int):
        self.chat_id = chat_id
        self.child = None
        self.model_name = "gemini-3.1-pro-high"
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
        return True

    async def _ensure_started(self):
        """Spawns process and drains startup banner to ensure it is ready for input."""
        if not self.child or not self.child.isalive():
            logger.info(f"Spawning agy PTY process for chat_id={self.chat_id} with model={self.model_name}")
            self.child = pexpect.spawn(
                AGY_BINARY_PATH,
                ["--model", self.model_name, "--dangerously-skip-permissions"],
                encoding="utf-8",
                echo=False,
                timeout=300
            )
            # Drain startup output until process settles (idle for 1.5s)
            idle_count = 0
            while idle_count < 3:
                try:
                    await asyncio.to_thread(self.child.read_nonblocking, size=512, timeout=0.5)
                    idle_count = 0
                except pexpect.TIMEOUT:
                    idle_count += 1
                except pexpect.EOF:
                    break

    async def stream_chat(self, prompt: str):
        """Sends a prompt to agy and yields chunks of text response."""
        async with self._lock:
            await self._ensure_started()

            clean_prompt = prompt.replace("\n", " ").strip()
            self.child.sendline(clean_prompt)

            accumulated = ""
            idle_count = 0

            while True:
                try:
                    chunk = await asyncio.to_thread(
                        self.child.read_nonblocking, size=512, timeout=0.5
                    )
                    if chunk:
                        clean_chunk = ansi_escape.sub('', chunk)
                        accumulated += clean_chunk
                        yield clean_chunk
                        idle_count = 0
                except pexpect.TIMEOUT:
                    idle_count += 1
                    # Turn finished if we received output and 2.5s passed
                    if accumulated and idle_count >= 5:
                        break
                    # Hard timeout if no output at all for 40s
                    if idle_count >= 80:
                        yield "\n⚠️ [Таймаут ответа от агента]"
                        break
                except pexpect.EOF:
                    logger.warning(f"Session for chat_id={self.chat_id} reached EOF.")
                    break

    def close(self):
        if self.child and self.child.isalive():
            try:
                self.child.close(force=True)
            except Exception as e:
                logger.error(f"Error closing session for chat_id={self.chat_id}: {e}")
            logger.info(f"Closed agy session for chat_id={self.chat_id}")
