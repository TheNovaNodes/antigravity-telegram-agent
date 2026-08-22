import logging
from typing import Dict, Optional
from aiogram import Bot

logger = logging.getLogger(__name__)

class BotRegistry:
    """Registry mapping bot_id to Bot instance for multi-bot message routing."""
    def __init__(self):
        self._bots: Dict[int, Bot] = {}

    def register(self, bot: Bot):
        if bot.id:
            self._bots[bot.id] = bot
            logger.info(f"Registered Bot instance in BotRegistry for bot_id={bot.id}")

    def get_bot(self, bot_id: int) -> Optional[Bot]:
        return self._bots.get(bot_id)

    def get_any_bot(self) -> Optional[Bot]:
        if self._bots:
            return next(iter(self._bots.values()))
        return None

    def clear(self):
        self._bots.clear()

bot_registry = BotRegistry()
