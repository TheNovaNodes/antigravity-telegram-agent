import os
from pathlib import Path
from dotenv import load_dotenv

load_dotenv()

raw_tokens = os.getenv("TELEGRAM_BOT_TOKENS") or os.getenv("TELEGRAM_BOT_TOKEN") or ""
BOT_TOKENS = [t.strip() for t in raw_tokens.split(",") if t.strip()]

if not BOT_TOKENS:
    raise ValueError("TELEGRAM_BOT_TOKEN or TELEGRAM_BOT_TOKENS is missing in environment")

BOT_TOKEN = BOT_TOKENS[0]

GEMINI_API_KEY = os.getenv("GEMINI_API_KEY")

ALLOWED_USER_IDS = set(
    int(uid.strip())
    for uid in os.getenv("ALLOWED_USER_IDS", "").split(",")
    if uid.strip()
)

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO")
AGY_BINARY_PATH = os.getenv("AGY_BINARY_PATH", "/root/.local/bin/agy")

WORKSPACE_BASE_PATHS = [
    Path(p.strip()).resolve()
    for p in os.getenv("WORKSPACE_BASE_PATHS", "/root/lab,/root/workspace").split(",")
    if p.strip()
]
