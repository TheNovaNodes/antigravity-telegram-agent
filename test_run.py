import asyncio
from src.cli_runner import AgySession
import logging

logging.basicConfig(level=logging.INFO)

async def main():
    s = AgySession(173681771, "gemini-3.6-flash-low", "low", "default")
    try:
        await s._ensure_started()
        s.child.send(b"test\r\n")
        import asyncio
        from src.formatters import extract_new_response_lines
        from src.cli_runner import _safe_screen_display
        for _ in range(10):
            await asyncio.sleep(1)
            raw = _safe_screen_display(s.screen)
            new = extract_new_response_lines(raw, "test")
            print("NEW:", [x for x in new if x.strip()])
    finally:
        s.close()

if __name__ == "__main__":
    asyncio.run(main())
