import asyncio
import logging
import os
import pexpect
import pyte
from typing import Optional
from src.config import AGY_BINARY_PATH
from src.formatters import extract_new_response_lines

logger = logging.getLogger(__name__)

async def run_shadow_prompt(prompt: str, workspace: Optional[str] = None, timeout: int = 60) -> str:
    """
    Executes a prompt in an ephemeral, headless shadow PTY session.
    Completely isolated from active interactive Telegram sessions.
    """
    cmd = f"{AGY_BINARY_PATH}"
    # Security: Do not inherit full os.environ
    env = {
        "PATH": os.environ.get("PATH", "/bin:/usr/bin"),
        "USER": os.environ.get("USER", "root"),
        "HOME": os.environ.get("HOME", "/root")
    }
    if workspace:
        env["AGY_WORKSPACE"] = workspace

    logger.info(f"Spawning Shadow PTY for prompt: {prompt[:40]}...")
    
    # Spawn ephemeral process
    child = pexpect.spawn(
        cmd,
        encoding="utf-8",
        codec_errors="replace",
        dimensions=(40, 500),
        env=env,
        timeout=timeout
    )
    screen = pyte.Screen(500, 40)
    stream = pyte.Stream(screen)

    try:
        # Give TUI a moment to initialize
        await asyncio.sleep(1.0)
        
        # Send prompt
        child.sendline(prompt)

        # Wait for response completion or idle (wait until LLM outputs real non-TUI response)
        start_time = asyncio.get_running_loop().time()
        last_display = []
        stable_count = 0
        
        while (asyncio.get_running_loop().time() - start_time) < timeout:
            await asyncio.sleep(0.5)
            try:
                data = child.read_nonblocking(size=4096, timeout=0.1)
                if data:
                    stream.feed(data)
            except (pexpect.TIMEOUT, pexpect.EOF):
                pass

            curr_display = screen.display
            clean_lines = extract_new_response_lines(curr_display, prompt=prompt)

            # Check if screen display stabilized AND we have actual non-TUI response content
            if curr_display == last_display and clean_lines:
                stable_count += 1
                if stable_count >= 3:
                    break
            else:
                stable_count = 0

            last_display = list(curr_display)

        clean_lines = extract_new_response_lines(screen.display, prompt=prompt)
        result_text = "\n".join(clean_lines).strip()
        return result_text if result_text else "\n".join([l for l in screen.display if l.strip()]).strip()

    except Exception as e:
        logger.error(f"Error in Shadow PTY execution: {e}", exc_info=True)
        return f"Error executing shadow prompt: {str(e)}"
    finally:
        try:
            if child.isalive():
                child.close(force=True)
        except Exception:
            pass
