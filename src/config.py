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

# Profile mappings parsing (TELEGRAM_BOT_PROFILES)
# Format: token1:profile1,token2:profile2 OR profile1,profile2 matched by index to BOT_TOKENS
from src.profile import BotProfile

raw_profiles = os.getenv("TELEGRAM_BOT_PROFILES", "").strip()
TOKEN_PROFILE_MAP: dict[str, str] = {}
BOT_ID_PROFILE_MAP: dict[int, BotProfile] = {}

if raw_profiles:
    entries = [e.strip() for e in raw_profiles.split(",") if e.strip()]
    for idx, entry in enumerate(entries):
        if ":" in entry:
            parts = entry.split(":", 1)
            token = parts[0].strip()
            prof_name = parts[1].strip()
            if token and prof_name:
                TOKEN_PROFILE_MAP[token] = prof_name
        else:
            if idx < len(BOT_TOKENS):
                TOKEN_PROFILE_MAP[BOT_TOKENS[idx]] = entry

def get_profile_for_bot(bot_id: int) -> BotProfile:
    """Retrieve or resolve the BotProfile for a given bot_id.
    Default primary bot (first token) uses 'default', secondary bots use 'profile_<bot_id>' if not explicitly mapped.
    """
    if bot_id in BOT_ID_PROFILE_MAP:
        return BOT_ID_PROFILE_MAP[bot_id]

    # Check if bot_id matches any known bot token in BOT_TOKENS by matching token ID prefix
    matched_prof_name = None
    for token in BOT_TOKENS:
        try:
            tok_id = int(token.split(":")[0])
            if tok_id == bot_id:
                matched_prof_name = TOKEN_PROFILE_MAP.get(token)
                break
        except (ValueError, IndexError):
            pass

    if not matched_prof_name:
        # Check if bot_id belongs to primary bot (BOT_TOKENS[0])
        primary_id = None
        if BOT_TOKENS:
            try:
                primary_id = int(BOT_TOKENS[0].split(":")[0])
            except (ValueError, IndexError):
                pass
        if primary_id and bot_id == primary_id:
            matched_prof_name = "default"
        else:
            matched_prof_name = f"profile_{bot_id}"

    profile = BotProfile(name=matched_prof_name, bot_id=bot_id)
    BOT_ID_PROFILE_MAP[bot_id] = profile
    return profile
