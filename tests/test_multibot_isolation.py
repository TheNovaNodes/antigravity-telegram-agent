import unittest
import tempfile
from pathlib import Path
from unittest.mock import patch, MagicMock

import src.db as db_module
from src.session_key import SessionKey
from src.session_manager import SessionManager
from src.scheduler import SentinelScheduler
from src.bot_registry import bot_registry


class TestMultiBotIsolation(unittest.TestCase):
    def setUp(self):
        self.tmp_dir = tempfile.TemporaryDirectory()
        self.db_file = Path(self.tmp_dir.name) / "test_multibot.db"
        db_module.DB_PATH = self.db_file
        db_module.reset_db_connection()
        db_module.init_db(default_bot_id=100)

    def tearDown(self):
        db_module.reset_db_connection()
        self.tmp_dir.cleanup()

    def test_session_manager_two_bots_same_chat_id(self):
        sm = SessionManager()
        chat_id = 999
        sk1 = SessionKey(bot_id=101, chat_id=chat_id)
        sk2 = SessionKey(bot_id=102, chat_id=chat_id)

        session1 = sm.get_session(sk1)
        session2 = sm.get_session(sk2)

        self.assertIsNot(session1, session2)
        session1.set_model("claude-sonnet-4-6")
        session2.set_model("gpt-oss-120b-medium")

        self.assertEqual(session1.model_name, "claude-sonnet-4-6")
        self.assertEqual(session2.model_name, "gpt-oss-120b-medium")

        # Reload from fresh SessionManager instance to verify DB persistence isolation
        sm_reloaded = SessionManager()
        loaded1 = sm_reloaded.get_session(sk1)
        loaded2 = sm_reloaded.get_session(sk2)

        self.assertEqual(loaded1.model_name, "claude-sonnet-4-6")
        self.assertEqual(loaded2.model_name, "gpt-oss-120b-medium")

    def test_legacy_sqlite_migration_idempotent(self):
        """Verify upgrading legacy table with single primary key `chat_id` preserves rows."""
        db_module.reset_db_connection()
        if self.db_file.exists():
            self.db_file.unlink()

        conn = db_module._get_connection()
        conn.execute("""
            CREATE TABLE user_sessions (
                chat_id INTEGER PRIMARY KEY,
                model_name TEXT NOT NULL,
                effort TEXT NOT NULL,
                mode TEXT NOT NULL,
                conversation_id TEXT,
                updated_at TEXT DEFAULT (datetime('now'))
            );
        """)
        conn.execute("""
            INSERT INTO user_sessions (chat_id, model_name, effort, mode, conversation_id)
            VALUES (777, 'legacy-model', 'low', 'default', 'conv-123');
        """)
        conn.commit()

        # Execute init_db migration specifying default_bot_id=200
        db_module.init_db(default_bot_id=200)

        sk_legacy = SessionKey(bot_id=200, chat_id=777)
        saved = db_module.load_user_session(sk_legacy)

        self.assertIsNotNone(saved)
        self.assertEqual(saved["model_name"], "legacy-model")
        self.assertEqual(saved["conversation_id"], "conv-123")

    def test_scheduler_job_isolation(self):
        scheduler = SentinelScheduler()
        bot1 = MagicMock(id=101)
        bot2 = MagicMock(id=102)

        bot_registry.register(bot1)
        bot_registry.register(bot2)

        scheduler.add_sentinel_job("daily_job", bot_id=101, chat_id=500, prompt="check logs", cron_expression="0 8 * * *")
        scheduler.add_sentinel_job("daily_job", bot_id=102, chat_id=500, prompt="check security", cron_expression="0 9 * * *")

        jobs = scheduler.list_jobs()
        job_ids = [j["id"] for j in jobs]

        self.assertEqual(len(job_ids), 2)
        self.assertIn("101:daily_job", job_ids)
        self.assertIn("102:daily_job", job_ids)


    def test_sentinel_fail_closed_on_unregistered_bot(self):
        """Verify SentinelScheduler does not fallback to arbitrary bot if bot_id is not registered."""
        scheduler = SentinelScheduler()
        bot1 = MagicMock(id=101)
        bot_registry.register(bot1)

        with patch("src.scheduler.run_shadow_prompt") as mock_shadow:
            import asyncio
            asyncio.run(scheduler.execute_sentinel_briefing(chat_id=500, prompt="test prompt", bot_id=999))
            mock_shadow.assert_not_called()
            bot1.send_message.assert_not_called()

    def test_handler_router_get_session_key_extraction(self):
        """Verify handler get_session_key extracts bot_id and chat_id correctly."""
        from src.handlers import get_session_key
        from aiogram.types import Message, Chat, CallbackQuery

        bot_a = MagicMock(id=1001)
        bot_b = MagicMock(id=1002)

        msg_a = MagicMock(spec=Message, chat=MagicMock(spec=Chat, id=444))
        msg_b = MagicMock(spec=Message, chat=MagicMock(spec=Chat, id=444))

        key_a = get_session_key(msg_a, bot_a)
        key_b = get_session_key(msg_b, bot_b)

        self.assertEqual(key_a, SessionKey(bot_id=1001, chat_id=444))
        self.assertEqual(key_b, SessionKey(bot_id=1002, chat_id=444))
        self.assertNotEqual(key_a, key_b)


if __name__ == "__main__":
    unittest.main()
