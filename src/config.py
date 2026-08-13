import os
from pathlib import Path
from dotenv import load_dotenv

load_dotenv()

BOT_TOKEN = os.getenv("TELEGRAM_BOT_TOKEN")
if not BOT_TOKEN:
    raise ValueError("TELEGRAM_BOT_TOKEN is missing in .env")

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
