import pytest
import asyncio
from unittest.mock import MagicMock, AsyncMock, patch
from src.shadow_runner import run_shadow_prompt
from src.scheduler import SentinelScheduler

@pytest.mark.asyncio
async def test_shadow_runner_execution():
    with patch("pexpect.spawn") as mock_spawn:
        mock_child = MagicMock()
        mock_child.read_nonblocking.side_effect = [
            "Initializing AGY Shadow...\n",
            "System Status: 100% Operational\n",
            ""
        ]
        mock_child.isalive.return_value = True
        mock_spawn.return_value = mock_child

        result = await run_shadow_prompt("Status check", timeout=2)
        assert result is not None
        mock_child.sendline.assert_called_once_with("Status check")
        mock_child.close.assert_called()

@pytest.mark.asyncio
async def test_sentinel_scheduler_job_lifecycle():
    scheduler = SentinelScheduler()
    mock_bot = AsyncMock()
    scheduler.set_bot(mock_bot)
    scheduler.start()

    try:
        scheduler.add_sentinel_job(
            job_id="test_job_1",
            chat_id=12345,
            prompt="Daily Health Summary",
            cron_expression="0 8 * * *"
        )
        jobs = scheduler.list_jobs()
        assert len(jobs) == 1
        assert jobs[0]["id"] == "test_job_1"

        # Trigger execution directly
        with patch("pexpect.spawn") as mock_spawn:
            mock_child = MagicMock()
            mock_child.read_nonblocking.side_effect = ["Output\n", ""]
            mock_child.isalive.return_value = True
            mock_spawn.return_value = mock_child
            
            await scheduler.execute_sentinel_briefing(12345, "Health Check")
            
        mock_bot.send_message.assert_called_once()
        assert "Autonomous Sentinel Briefing" in mock_bot.send_message.call_args[1]["text"]

        removed = scheduler.remove_sentinel_job("test_job_1")
        assert removed is True
        assert len(scheduler.list_jobs()) == 0
    finally:
        scheduler.shutdown()
