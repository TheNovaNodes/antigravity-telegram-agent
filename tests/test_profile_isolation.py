import unittest
import tempfile
import sqlite3
import os
from pathlib import Path
from unittest.mock import patch, MagicMock

from src.profile import BotProfile, PROFILES_ROOT, sanitize_profile_name
from src.config import get_profile_for_bot, BOT_ID_PROFILE_MAP, TOKEN_PROFILE_MAP
from src.conversations import (
    get_available_conversations,
    rename_conversation,
    get_latest_conversation_id,
    get_conversation_title,
)
from src.handlers import get_resume_keyboard


class TestProfileIsolation(unittest.TestCase):
    def setUp(self):
        self.tmp_dir = tempfile.TemporaryDirectory()
        self.tmp_path = Path(self.tmp_dir.name)
        BOT_ID_PROFILE_MAP.clear()
        TOKEN_PROFILE_MAP.clear()

    def tearDown(self):
        BOT_ID_PROFILE_MAP.clear()
        TOKEN_PROFILE_MAP.clear()
        self.tmp_dir.cleanup()

    def test_path_traversal_protection(self):
        """Test path traversal protection raises ValueError when escaping allowed root."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            # Normal names work fine
            p1 = BotProfile("test_bot")
            self.assertTrue(p1.state_dir.is_relative_to((self.tmp_path / "profiles").resolve()))

            # Path traversal attempts must raise ValueError
            with self.assertRaises(ValueError):
                BotProfile("../../../etc")

            with self.assertRaises(ValueError):
                BotProfile("../../secrets")

            with self.assertRaises(ValueError):
                BotProfile("/absolute/path")

    def test_profile_permissions(self):
        """Test strict permissions (0700 for dirs, 0600 for files) are set."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("secure_bot")
            self.assertTrue(prof.state_dir.exists())

            # Check directory permissions
            mode = prof.state_dir.stat().st_mode & 0o777
            self.assertEqual(mode, 0o700)

            # Check file permissions
            dummy_file = prof.state_dir / "test.txt"
            dummy_file.write_text("secret_data")
            prof.enforce_file_permissions(dummy_file)

            file_mode = dummy_file.stat().st_mode & 0o777
            self.assertEqual(file_mode, 0o600)

    def test_multi_profile_isolation(self):
        """Test two bots with distinct profiles write to separate DBs and brain paths."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("alpha_bot", bot_id=101)
            prof_b = BotProfile("beta_bot", bot_id=102)

            self.assertNotEqual(prof_a.state_dir, prof_b.state_dir)
            self.assertTrue((prof_a.state_dir / "brain").exists())
            self.assertTrue((prof_b.state_dir / "brain").exists())

            # Create SQLite DB in profile A
            db_a = prof_a.state_dir / "conversation_summaries.db"
            with sqlite3.connect(str(db_a)) as conn:
                conn.execute("""
                    CREATE TABLE conversation_summaries (
                        conversation_id TEXT PRIMARY KEY,
                        preview TEXT,
                        title TEXT,
                        step_count INTEGER,
                        last_modified_time TEXT
                    )
                """)
                conn.execute("""
                    INSERT INTO conversation_summaries (conversation_id, preview, title, step_count, last_modified_time)
                    VALUES ('conv-alpha-1', 'Alpha Preview', 'Alpha Title', 5, '2026-08-22 12:00:00')
                """)
                conn.commit()

            # Create SQLite DB in profile B
            db_b = prof_b.state_dir / "conversation_summaries.db"
            with sqlite3.connect(str(db_b)) as conn:
                conn.execute("""
                    CREATE TABLE conversation_summaries (
                        conversation_id TEXT PRIMARY KEY,
                        preview TEXT,
                        title TEXT,
                        step_count INTEGER,
                        last_modified_time TEXT
                    )
                """)
                conn.execute("""
                    INSERT INTO conversation_summaries (conversation_id, preview, title, step_count, last_modified_time)
                    VALUES ('conv-beta-1', 'Beta Preview', 'Beta Title', 3, '2026-08-22 12:05:00')
                """)
                conn.commit()

            # Query conversations for Alpha
            convs_a = get_available_conversations(profile=prof_a)
            self.assertEqual(len(convs_a), 1)
            self.assertEqual(convs_a[0]["id"], "conv-alpha-1")
            self.assertEqual(convs_a[0]["summary"], "Alpha Title")

            # Query conversations for Beta
            convs_b = get_available_conversations(profile=prof_b)
            self.assertEqual(len(convs_b), 1)
            self.assertEqual(convs_b[0]["id"], "conv-beta-1")
            self.assertEqual(convs_b[0]["summary"], "Beta Title")

            # Verify latest IDs are isolated
            self.assertEqual(get_latest_conversation_id(profile=prof_a), "conv-alpha-1")
            self.assertEqual(get_latest_conversation_id(profile=prof_b), "conv-beta-1")

    def test_resume_menu_filtering_by_profile(self):
        """Test /resume menu filtering by profile."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=201)
            prof_b = BotProfile("bot_b", bot_id=202)

            db_a = prof_a.state_dir / "conversation_summaries.db"
            with sqlite3.connect(str(db_a)) as conn:
                conn.execute("""
                    CREATE TABLE conversation_summaries (
                        conversation_id TEXT PRIMARY KEY,
                        preview TEXT,
                        title TEXT,
                        step_count INTEGER,
                        last_modified_time TEXT
                    )
                """)
                conn.execute("""
                    INSERT INTO conversation_summaries (conversation_id, preview, title, step_count, last_modified_time)
                    VALUES ('11112222-3333-4444-5555-666677778888', 'Dialog A', 'Title A', 10, '2026-08-22 13:00:00')
                """)
                conn.commit()

            kb_a = get_resume_keyboard(profile=prof_a)
            kb_b = get_resume_keyboard(profile=prof_b)

            # Keyboard for A should contain conv 11112222-3333-4444-5555-666677778888
            buttons_a = [btn.text for row in kb_a.inline_keyboard for btn in row]
            self.assertTrue(any("1111" in text or "Title A" in text for text in buttons_a))

            # Keyboard for B should NOT contain conv A
            buttons_b = [btn.text for row in kb_b.inline_keyboard for btn in row]
            self.assertFalse(any("1111" in text or "Title A" in text for text in buttons_b))


if __name__ == "__main__":
    unittest.main()
