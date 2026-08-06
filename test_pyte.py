import pexpect
import os
import pyte

env = os.environ.copy()
env["TERM"] = "xterm"

print("Spawning agy...")
child = pexpect.spawn(
    "/root/.local/bin/agy",
    ["--dangerously-skip-permissions"],
    env=env,
    echo=False,
    timeout=300
)

screen = pyte.Screen(120, 50)
stream = pyte.ByteStream(screen)

# Drain banner
idle = 0
while idle < 3:
    try:
        chunk = child.read_nonblocking(size=1024, timeout=0.5)
        stream.feed(chunk)
        idle = 0
    except pexpect.TIMEOUT:
        idle += 1

print("Sending prompt...")
child.send("Привет! Напиши мне 3 коротких факта о Космосе.\r\n".encode("utf-8"))

screen = pyte.Screen(120, 50)
stream = pyte.ByteStream(screen)

idle = 0
while True:
    try:
        chunk = child.read_nonblocking(size=1024, timeout=0.5)
        if chunk:
            stream.feed(chunk)
            idle = 0
    except pexpect.TIMEOUT:
        idle += 1
        if idle >= 6: # Idle for 3 seconds
            break
    except pexpect.EOF:
        break

print("\n--- PYTE VIRTUAL TERMINAL SCREEN DISPLAY ---")
clean_lines = []
for line in screen.display:
    l = line.rstrip()
    if l.strip():
        # Remove horizontal rules, prompts, UI footers
        if "────" in l or "esc to cancel" in l or "Generating..." in l:
            continue
        if l.strip().startswith(">") or l.strip().startswith("Gemini") or l.strip().startswith("Claude"):
            continue
        clean_lines.append(l)

final_text = "\n".join(clean_lines)
print(final_text)
print("--- END ---")

child.close()
