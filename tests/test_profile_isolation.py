import unittest
import tempfile
import sqlite3
import os
import asyncio
from pathlib import Path
from unittest.mock import patch, MagicMock

from src.profile import BotProfile, PROFILES_ROOT, sanitize_profile_name, migrate_legacy_shared_state
from src.config import get_profile_for_bot, BOT_ID_PROFILE_MAP, TOKEN_PROFILE_MAP
from src.conversations import (
    get_available_conversations,
    rename_conversation,
    get_latest_conversation_id,
    get_conversation_title,
)
from src.handlers import get_resume_keyboard, check_and_send_artifacts
from src.cli_runner import AgySession, get_active_account_email, get_auth_state_signature
from src.shadow_runner import run_shadow_prompt
from src.scheduler import SentinelScheduler
from src.mcp_config import MCPConfigManager


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
        """Test path traversal and symlink protection raises ValueError when escaping allowed root."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            p1 = BotProfile("test_bot")
            self.assertTrue(p1.state_dir.is_relative_to((self.tmp_path / "profiles").resolve()))

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

            mode = prof.state_dir.stat().st_mode & 0o777
            self.assertEqual(mode, 0o700)

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

            convs_a = get_available_conversations(profile=prof_a)
            self.assertEqual(len(convs_a), 1)
            self.assertEqual(convs_a[0]["id"], "conv-alpha-1")
            self.assertEqual(convs_a[0]["summary"], "Alpha Title")

            convs_b = get_available_conversations(profile=prof_b)
            self.assertEqual(len(convs_b), 1)
            self.assertEqual(convs_b[0]["id"], "conv-beta-1")
            self.assertEqual(convs_b[0]["summary"], "Beta Title")

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

            buttons_a = [btn.text for row in kb_a.inline_keyboard for btn in row]
            self.assertTrue(any("1111" in text or "Title A" in text for text in buttons_a))

            buttons_b = [btn.text for row in kb_b.inline_keyboard for btn in row]
            self.assertFalse(any("1111" in text or "Title A" in text for text in buttons_b))

    def test_pty_env_and_home_profile_isolation(self):
        """Test interactive PTY env and HOME are isolated per profile."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=301)
            prof_b = BotProfile("bot_b", bot_id=302)

            session_a = AgySession(chat_id=123, profile=prof_a)
            session_b = AgySession(chat_id=123, profile=prof_b)

            with patch("src.cli_runner.pexpect.spawn") as mock_spawn:
                mock_child = MagicMock()
                mock_child.isalive.return_value = True
                mock_child.read_nonblocking.return_value = b"> "
                mock_spawn.return_value = mock_child

                asyncio.run(session_a._ensure_started())
                args_a, kwargs_a = mock_spawn.call_args
                env_a = kwargs_a.get("env", {})

                asyncio.run(session_b._ensure_started())
                args_b, kwargs_b = mock_spawn.call_args
                env_b = kwargs_b.get("env", {})

                self.assertEqual(env_a["HOME"], str(prof_a.state_dir))
                self.assertEqual(env_a["AGY_PROFILE_DIR"], str(prof_a.state_dir))
                self.assertEqual(env_b["HOME"], str(prof_b.state_dir))
                self.assertEqual(env_b["AGY_PROFILE_DIR"], str(prof_b.state_dir))
                self.assertNotEqual(env_a["HOME"], env_b["HOME"])

    def test_shadow_job_execution_profile_routing(self):
        """Test shadow job execution and delivery pass profile and route env through profile.state_dir."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=401)
            prof_b = BotProfile("bot_b", bot_id=402)

            with patch("src.shadow_runner.pexpect.spawn") as mock_spawn:
                mock_child = MagicMock()
                mock_child.isalive.return_value = False
                mock_child.read_nonblocking.side_effect = Exception("EOF")
                mock_spawn.return_value = mock_child

                asyncio.run(run_shadow_prompt("test prompt", profile=prof_a))
                env_a = mock_spawn.call_args[1].get("env", {})

                asyncio.run(run_shadow_prompt("test prompt", profile=prof_b))
                env_b = mock_spawn.call_args[1].get("env", {})

                self.assertEqual(env_a["HOME"], str(prof_a.state_dir))
                self.assertEqual(env_a["AGY_PROFILE_DIR"], str(prof_a.state_dir))
                self.assertEqual(env_b["HOME"], str(prof_b.state_dir))
                self.assertEqual(env_b["AGY_PROFILE_DIR"], str(prof_b.state_dir))

            # Test Sentinel scheduler profile job definition
            scheduler = SentinelScheduler()
            scheduler.add_sentinel_job("job1", chat_id=123, prompt="p", cron_expression="0 * * * *", bot_id=401, profile_name="bot_a")
            jobs = scheduler.list_jobs()
            self.assertEqual(len(jobs), 1)
            self.assertEqual(jobs[0]["args"][3], "bot_a")

    def test_auth_lookups_and_signatures_profile_isolation(self):
        """Test auth lookups and status signatures inspect profile.state_dir."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=501)
            prof_b = BotProfile("bot_b", bot_id=502)

            # Put custom settings file in prof_a
            settings_a = prof_a.state_dir / "settings.json"
            settings_a.write_text('{"email": "alpha@example.com"}')

            settings_b = prof_b.state_dir / "settings.json"
            settings_b.write_text('{"email": "beta@example.com"}')

            self.assertEqual(get_active_account_email(profile=prof_a), "alpha@example.com")
            self.assertEqual(get_active_account_email(profile=prof_b), "beta@example.com")

            # Create token file in prof_a
            token_a = prof_a.state_dir / "antigravity-oauth-token"
            token_a.write_bytes(b"token_alpha_data")

            token_b = prof_b.state_dir / "antigravity-oauth-token"
            token_b.write_bytes(b"token_beta_data_different")

            sig_a = get_auth_state_signature(profile=prof_a)
            sig_b = get_auth_state_signature(profile=prof_b)
            self.assertNotEqual(sig_a, sig_b)

    def test_mcp_config_profile_scoping(self):
        """Test MCP config is profile-scoped at profile.state_dir / mcp_config.json."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=601)
            prof_b = BotProfile("bot_b", bot_id=602)

            mcp_a = MCPConfigManager(profile=prof_a)
            mcp_b = MCPConfigManager(profile=prof_b)

            self.assertEqual(mcp_a.config_path, prof_a.state_dir / "mcp_config.json")
            self.assertEqual(mcp_b.config_path, prof_b.state_dir / "mcp_config.json")

            mcp_a.toggle_server("searxng")
            self.assertTrue((prof_a.state_dir / "mcp_config.json").exists())
            self.assertFalse((prof_b.state_dir / "mcp_config.json").exists())

    def test_artifact_discovery_and_debug_profile_isolation(self):
        """Test artifact discovery and /debug output per profile."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("bot_a", bot_id=701)
            prof_b = BotProfile("bot_b", bot_id=702)

            session_a = AgySession(chat_id=100, profile=prof_a)
            session_b = AgySession(chat_id=100, profile=prof_b)

            # Create artifact in prof_a artifacts dir
            art_dir_a = prof_a.state_dir / "artifacts"
            art_dir_a.mkdir(parents=True, exist_ok=True)
            art_file_a = art_dir_a / "report_a.txt"
            art_file_a.write_text("Profile A artifact report")

            art_dir_b = prof_b.state_dir / "artifacts"
            art_dir_b.mkdir(parents=True, exist_ok=True)
            art_file_b = art_dir_b / "report_b.txt"
            art_file_b.write_text("Profile B artifact report")

            mock_message_a = MagicMock()
            mock_message_a.chat.id = 100
            mock_message_a.answer_document = MagicMock()

            asyncio.run(check_and_send_artifacts(mock_message_a, session_a))

            # Verify answer_document was called for report_a.txt
            mock_message_a.answer_document.assert_called_once()
            call_kwargs = mock_message_a.answer_document.call_args[1]
            self.assertIn("report_a.txt", call_kwargs["caption"])
            self.assertNotIn("report_b.txt", call_kwargs["caption"])

    def test_migrate_legacy_shared_state(self):
        """Test migrate_legacy_shared_state backs up legacy DB with .bak before moving to profile."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("default")
            legacy_dir = self.tmp_path / "legacy_gemini"
            legacy_dir.mkdir(parents=True, exist_ok=True)
            legacy_db = legacy_dir / "conversation_summaries.db"
            legacy_db.write_text("legacy db content")

            with patch("pathlib.Path.home", return_value=self.tmp_path):
                # recreate legacy location under fake home
                fake_legacy = self.tmp_path / ".gemini" / "antigravity-cli"
                fake_legacy.mkdir(parents=True, exist_ok=True)
                fake_db = fake_legacy / "conversation_summaries.db"
                fake_db.write_text("legacy db content")

                migrate_legacy_shared_state(prof)

                backup_db = fake_legacy / "conversation_summaries.db.bak"
                target_db = prof.state_dir / "conversation_summaries.db"

                self.assertTrue(backup_db.exists())
                self.assertTrue(target_db.exists())
                self.assertFalse(fake_db.exists())
                self.assertEqual(target_db.read_text(), "legacy db content")
                self.assertEqual(backup_db.read_text(), "legacy db content")


if __name__ == "__main__":
    unittest.main()
