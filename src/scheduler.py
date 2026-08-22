import logging
from typing import Optional
from aiogram import Bot
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from src.shadow_runner import run_shadow_prompt
from src.bot_registry import bot_registry
from src.profile import BotProfile
from src.config import get_profile_for_bot

logger = logging.getLogger(__name__)

class SentinelScheduler:
    """Manager for autonomous background jobs and scheduled AI sentinel alerts."""
    def __init__(self):
        self.scheduler = AsyncIOScheduler()

    def set_bot(self, bot: Bot):
        """Backward compatibility setter registering bot in BotRegistry."""
        bot_registry.register(bot)

    def start(self):
        if not self.scheduler.running:
            self.scheduler.start()
            logger.info("Sentinel AsyncIOScheduler started successfully.")

    def shutdown(self):
        if self.scheduler.running:
            self.scheduler.shutdown(wait=False)
            logger.info("Sentinel AsyncIOScheduler shut down.")

    async def execute_sentinel_briefing(self, chat_id: int, prompt: str, bot_id: int = 0, profile_name: Optional[str] = None):
        """Runs a shadow PTY LLM briefing and sends the AI summary directly to Telegram."""
        bot = bot_registry.get_bot(bot_id)
        if not bot:
            logger.error(f"Cannot execute sentinel briefing for bot_id={bot_id}: Specific bot instance not registered (fail-closed).")
            return

        profile = BotProfile(name=profile_name, bot_id=bot_id) if profile_name else get_profile_for_bot(bot_id)
        logger.info(f"Triggering Autonomous Sentinel Briefing for bot_id={bot_id}, chat_id={chat_id}, profile={profile.name}")
        summary = await run_shadow_prompt(prompt, profile=profile)
        
        message_text = f"🤖 **[Autonomous Sentinel Briefing]**\n\n{summary}"
        try:
            await bot.send_message(chat_id=chat_id, text=message_text, parse_mode="Markdown")
            logger.info(f"Sentinel Briefing delivered for bot_id={bot_id}, chat_id={chat_id}")
        except Exception as e:
            logger.error(f"Failed to deliver Sentinel Briefing for bot_id={bot_id}, chat_id={chat_id}: {e}")

    def add_sentinel_job(self, job_id: str, chat_id: int, prompt: str, cron_expression: str, bot_id: int = 0, profile_name: Optional[str] = None):
        """Schedules a recurring autonomous sentinel job scoped to bot_id and profile."""
        parts = cron_expression.split()
        if len(parts) != 5:
            raise ValueError("Cron expression must have 5 fields (minute hour day month day_of_week)")

        minute, hour, day, month, day_of_week = parts
        full_job_id = f"{bot_id}:{job_id}" if ":" not in job_id and bot_id != 0 else job_id

        if not profile_name:
            profile_name = get_profile_for_bot(bot_id).name

        self.scheduler.add_job(
            self.execute_sentinel_briefing,
            trigger='cron',
            id=full_job_id,
            replace_existing=True,
            minute=minute,
            hour=hour,
            day=day,
            month=month,
            day_of_week=day_of_week,
            args=[chat_id, prompt, bot_id, profile_name]
        )
        logger.info(f"Added Sentinel Job '{full_job_id}' for bot_id={bot_id}, profile={profile_name}, chat_id={chat_id} with cron '{cron_expression}'")

    def remove_sentinel_job(self, job_id: str) -> bool:
        if self.scheduler.get_job(job_id):
            self.scheduler.remove_job(job_id)
            logger.info(f"Removed Sentinel Job '{job_id}'")
            return True
        return False

    def list_jobs(self) -> list:
        res = []
        for job in self.scheduler.get_jobs():
            next_run = getattr(job, "next_run_time", None)
            res.append({
                "id": job.id,
                "next_run_time": str(next_run) if next_run else "None",
                "args": job.args
            })
        return res

sentinel_scheduler = SentinelScheduler()
