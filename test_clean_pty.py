import pexpect
import os
import re

ansi_escape = re.compile(r'(?:\x1B[@-_]|[\x80-\x9F])[0-?]*[ -/]*[@-~]')

env = os.environ.copy()
env["TERM"] = "dumb"
env["NO_COLOR"] = "1"

print("Spawning agy with TERM=dumb NO_COLOR=1...")
child = pexpect.spawn(
    "/root/.local/bin/agy",
    ["--dangerously-skip-permissions"],
    env=env,
    encoding="utf-8",
    echo=False,
    timeout=300
)

# Drain banner
idle = 0
while idle < 3:
    try:
        chunk = child.read_nonblocking(size=512, timeout=0.5)
        idle = 0
    except pexpect.TIMEOUT:
        idle += 1

print("Sending 'Привет'...")
child.send("Привет\r\n")

accumulated = ""
idle = 0
while True:
    try:
        chunk = child.read_nonblocking(size=512, timeout=0.5)
        if chunk:
            clean = ansi_escape.sub('', chunk)
            accumulated += clean
            idle = 0
    except pexpect.TIMEOUT:
        idle += 1
        if accumulated and idle >= 5:
            break
    except pexpect.EOF:
        break

print("\n--- CLEAN OUTPUT START ---")
print(repr(accumulated))
print("--- CLEAN OUTPUT END ---")

child.close()
