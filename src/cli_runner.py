import asyncio
import logging
import re
import pexpect
from src.config import AGY_BINARY_PATH

logger = logging.getLogger(__name__)

ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')

class AgySession:
    """Manages an interactive PTY session for a single chat."""
    def __init__(self, chat_id: int):
        self.chat_id = chat_id
        self.child = None
        self._lock = asyncio.Lock()

    def start(self):
        if self.child and self.child.isalive():
            return
        logger.info(f"Spawning agy PTY process for chat_id={self.chat_id}")
        self.child = pexpect.spawn(
            AGY_BINARY_PATH,
            ["--dangerously-skip-permissions"],
            encoding="utf-8",
            echo=False,
            timeout=300
        )

    async def stream_chat(self, prompt: str):
        """Sends a prompt to agy and yields chunks of text response."""
        async with self._lock:
            if not self.child or not self.child.isalive():
                self.start()
                await asyncio.sleep(1.5)  # Wait for startup banner

            # Clean newlines from prompt to avoid multi-line split issues
            clean_prompt = prompt.replace("\n", " ").strip()
            self.child.sendline(clean_prompt)

            accumulated = ""
            idle_count = 0

            while True:
                try:
                    # Non-blocking read run in worker thread to not block event loop
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
                    # If we got response and have been idle for 2.5s, agent turn is complete
                    if accumulated and idle_count >= 5:
                        break
                    # If no response at all for 40s, timeout
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
