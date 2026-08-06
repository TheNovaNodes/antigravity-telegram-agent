import os
import unittest
from pathlib import Path
from src.db import init_db, save_user_session, load_user_session, delete_user_session, DB_PATH
from src.session_manager import SessionManager


class TestDBPersistence(unittest.TestCase):
    def setUp(self):
        init_db()

    def test_save_and_load_session(self):
        chat_id = 99912345
        save_user_session(chat_id, "claude-sonnet-4-6", "low", "plan")

        loaded = load_user_session(chat_id)
        self.assertIsNotNone(loaded)
        self.assertEqual(loaded["model_name"], "claude-sonnet-4-6")
        self.assertEqual(loaded["effort"], "low")
        self.assertEqual(loaded["mode"], "plan")

    def test_session_manager_auto_restoration(self):
        chat_id = 88812345
        # Save custom settings directly
        save_user_session(chat_id, "claude-opus-4-6-thinking", "medium", "accept-edits")

        # Create a new SessionManager instance simulating bot restart / redeploy
        sm = SessionManager()
        session = sm.get_session(chat_id)

        self.assertEqual(session.model_name, "claude-opus-4-6-thinking")
        self.assertEqual(session.effort, "medium")
        self.assertEqual(session.mode, "accept-edits")

    def test_reset_session_removes_from_db(self):
        chat_id = 77712345
        save_user_session(chat_id, "gpt-oss-120b-medium", "high", "default")

        sm = SessionManager()
        sm.get_session(chat_id)
        sm.reset_session(chat_id)

        loaded = load_user_session(chat_id)
        self.assertIsNone(loaded)


if __name__ == "__main__":
    unittest.main()
