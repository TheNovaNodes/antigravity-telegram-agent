import asyncio
from src.cli_runner import AgySession

async def test():
    session = AgySession("test_session")
    session.start()
    await asyncio.sleep(3) # wait for startup
    session.child.send(b"/usage\r\n")
    await asyncio.sleep(2)
    # Dump screen
    screen_lines = session.screen.display
    for i, line in enumerate(screen_lines):
        if line.strip():
            print(f"{i}: {line}")
    session.close()

if __name__ == "__main__":
    asyncio.run(test())
