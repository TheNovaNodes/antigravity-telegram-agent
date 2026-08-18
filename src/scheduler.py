import logging
from typing import Optional
from aiogram import Bot
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from src.shadow_runner import run_shadow_prompt

logger = logging.getLogger(__name__)

class SentinelScheduler:
    """Manager for autonomous background jobs and scheduled AI sentinel alerts."""
    def __init__(self):
        self.scheduler = AsyncIOScheduler()
        self.bot: Optional[Bot] = None

    def set_bot(self, bot: Bot):
        self.bot = bot

    def start(self):
        if not self.scheduler.running:
            self.scheduler.start()
            logger.info("Sentinel AsyncIOScheduler started successfully.")

    def shutdown(self):
        if self.scheduler.running:
            self.scheduler.shutdown(wait=False)
            logger.info("Sentinel AsyncIOScheduler shut down.")

    async def execute_sentinel_briefing(self, chat_id: int, prompt: str):
        """Runs a shadow PTY LLM briefing and sends the AI summary directly to Telegram."""
        if not self.bot:
            logger.error("Cannot execute sentinel briefing: Bot instance is not configured.")
            return

        logger.info(f"Triggering Autonomous Sentinel Briefing for chat_id={chat_id}")
        summary = await run_shadow_prompt(prompt)
        
        message_text = f"🤖 **[Autonomous Sentinel Briefing]**\n\n{summary}"
        try:
            await self.bot.send_message(chat_id=chat_id, text=message_text, parse_mode="Markdown")
            logger.info(f"Sentinel Briefing delivered to chat_id={chat_id}")
        except Exception as e:
            logger.error(f"Failed to deliver Sentinel Briefing to chat_id={chat_id}: {e}")

    def add_sentinel_job(self, job_id: str, chat_id: int, prompt: str, cron_expression: str):
        """Schedules a recurring autonomous sentinel job."""
        # Simple cron parser format: e.g. "0 8 * * *" (minute hour day month day_of_week)
        parts = cron_expression.split()
        if len(parts) != 5:
            raise ValueError("Cron expression must have 5 fields (minute hour day month day_of_week)")

        minute, hour, day, month, day_of_week = parts
        self.scheduler.add_job(
            self.execute_sentinel_briefing,
            trigger='cron',
            id=job_id,
            replace_existing=True,
            minute=minute,
            hour=hour,
            day=day,
            month=month,
            day_of_week=day_of_week,
            args=[chat_id, prompt]
        )
        logger.info(f"Added Sentinel Job '{job_id}' for chat_id={chat_id} with cron '{cron_expression}'")

    def remove_sentinel_job(self, job_id: str) -> bool:
        if self.scheduler.get_job(job_id):
            self.scheduler.remove_job(job_id)
            logger.info(f"Removed Sentinel Job '{job_id}'")
            return True
        return False

    def list_jobs(self) -> list:
        return [
            {
                "id": job.id,
                "next_run_time": str(job.next_run_time),
                "args": job.args
            }
            for job in self.scheduler.get_jobs()
        ]

sentinel_scheduler = SentinelScheduler()
