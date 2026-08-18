import sys
import time
import pexpect
import pyte
import os

env = os.environ.copy()
env["TERM"] = "xterm-256color"

child = pexpect.spawn("/root/.local/bin/agy", encoding=None, env=env)
child.setwinsize(120, 300)

time.sleep(2)
child.send(b"\r\n")
time.sleep(2)
child.send(b"/usage\r\n")
time.sleep(2)

screen = pyte.Screen(120, 300)
stream = pyte.ByteStream(screen)

while True:
    try:
        chunk = child.read_nonblocking(size=4096, timeout=1)
        if chunk:
            chunk = chunk.replace(b"\x1b[=1;1u", b"").replace(b"\x1b[>4;2m", b"")
            stream.feed(chunk)
    except pexpect.TIMEOUT:
        break
    except Exception:
        break

with open("test_output.txt", "w", encoding="utf-8") as f:
    for line in screen.display:
        f.write(line + "\n")

child.close(force=True)
