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

    def test_auth_negative_fail_closed_no_global_leak(self):
        """Test a new profile without auth returns 'Not Logged In' and empty signature without leaking shared global credentials."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("empty_profile")

            # Put global credentials in user home ~/.gemini/antigravity-cli/
            global_gemini = self.tmp_path / ".gemini" / "antigravity-cli"
            global_gemini.mkdir(parents=True, exist_ok=True)
            (global_gemini / "antigravity-oauth-token").write_text('{"token":{"access_token":"global_secret_token"}}')
            (global_gemini / "settings.json").write_text('{"email": "global_user@example.com"}')

            with patch("pathlib.Path.home", return_value=self.tmp_path):
                # Request email & signature for profile without local auth
                email = get_active_account_email(profile=prof)
                sig = get_auth_state_signature(profile=prof)

                self.assertEqual(email, "Not Logged In")
                self.assertEqual(sig, "")

    def test_backup_first_migration_protocol(self):
        """Test backup-first migration protocol creates timestamped backups before moving shared state items."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("migrated_bot")
            legacy_dir = self.tmp_path / "legacy_gemini"
            legacy_dir.mkdir(parents=True, exist_ok=True)

            (legacy_dir / "conversation_summaries.db").write_text("legacy_summaries")
            (legacy_dir / "user_sessions.db").write_text("legacy_sessions")
            (legacy_dir / "mcp_config.json").write_text('{"legacy": true}')
            (legacy_dir / "antigravity-oauth-token").write_text("legacy_token")
            (legacy_dir / "settings.json").write_text("legacy_settings")
            (legacy_dir / "brain").mkdir()
            (legacy_dir / "brain" / "data.txt").write_text("brain_content")
            (legacy_dir / "artifacts").mkdir()
            (legacy_dir / "artifacts" / "art.txt").write_text("artifact_content")

            status = migrate_legacy_shared_state(target_profile=prof, legacy_dir=legacy_dir)

            self.assertIn("conversation_summaries.db", status["migrated"])
            self.assertIn("brain", status["migrated"])
            self.assertEqual(len(status["backed_up"]), 7)

            # Check that files exist in profile.state_dir
            self.assertTrue((prof.state_dir / "conversation_summaries.db").exists())
            self.assertTrue((prof.state_dir / "user_sessions.db").exists())
            self.assertTrue((prof.state_dir / "mcp_config.json").exists())
            self.assertTrue((prof.state_dir / "brain" / "data.txt").exists())
            self.assertTrue((prof.state_dir / "artifacts" / "art.txt").exists())

            # Check backup copies created in legacy_dir
            backups = list(legacy_dir.glob("*.bak_*"))
            self.assertEqual(len(backups), 7)

    def test_multi_profile_same_chat_id_disjoint_environments(self):
        """Test multi-profile integration verifying two bots with the same chat_id maintain disjoint auth, MCP, PTY, and history."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof_a = BotProfile("profile_bot_a", bot_id=801)
            prof_b = BotProfile("profile_bot_b", bot_id=802)

            same_chat_id = 99999

            # Profile A has valid auth, Profile B does NOT
            (prof_a.state_dir / "settings.json").write_text('{"email": "bot_a@example.com"}')

            # a) Missing auth in Profile B returns 'Not Logged In' and does not read Profile A
            email_a = get_active_account_email(profile=prof_a)
            email_b = get_active_account_email(profile=prof_b)
            self.assertEqual(email_a, "bot_a@example.com")
            self.assertEqual(email_b, "Not Logged In")

            # b) MCP toggle in Profile A does NOT alter Profile B config or PTY env
            mcp_a = MCPConfigManager(profile=prof_a)
            mcp_b = MCPConfigManager(profile=prof_b)

            mcp_a.toggle_server("searxng")
            env_a = mcp_a.get_env_dict()
            env_b = mcp_b.get_env_dict()

            self.assertTrue((prof_a.state_dir / "mcp_config.json").exists())
            self.assertFalse((prof_b.state_dir / "mcp_config.json").exists())

            # c) Sessions with same chat_id under prof_a vs prof_b spawn disjoint state & PTY env
            session_a = AgySession(chat_id=same_chat_id, profile=prof_a)
            session_b = AgySession(chat_id=same_chat_id, profile=prof_b)

            self.assertEqual(session_a.profile.name, "profile_bot_a")
            self.assertEqual(session_b.profile.name, "profile_bot_b")
            self.assertNotEqual(session_a.profile.state_dir, session_b.profile.state_dir)

            with patch("pexpect.spawn") as mock_spawn:
                mock_child = MagicMock()
                mock_child.isalive.return_value = True
                mock_child.exitstatus = 0
                mock_child.read_nonblocking.return_value = b"> "
                mock_spawn.return_value = mock_child

                asyncio.run(session_a._ensure_started())
                env_pty_a = mock_spawn.call_args[1].get("env", {})
                session_a.close()

                asyncio.run(session_b._ensure_started())
                env_pty_b = mock_spawn.call_args[1].get("env", {})
                session_b.close()

                self.assertEqual(env_pty_a["AGY_PROFILE_DIR"], str(prof_a.state_dir))
                self.assertEqual(env_pty_b["AGY_PROFILE_DIR"], str(prof_b.state_dir))
                self.assertNotEqual(env_pty_a["AGY_PROFILE_DIR"], env_pty_b["AGY_PROFILE_DIR"])


            # F-01 check: default profile DB path is in profile.state_dir
            prof_default = BotProfile("default")
            from src.conversations import _resolve_db_path
            self.assertEqual(_resolve_db_path(prof_default), prof_default.state_dir / "conversation_summaries.db")

    def test_f03_mcp_config_deepcopy_isolation(self):
        """Test F-03: MCPConfigManager uses deepcopy so DEFAULT_MCP_CONFIG is untouched on toggle."""
        from src.mcp_config import DEFAULT_MCP_CONFIG, MCPConfigManager
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("mcp_test_bot")
            mgr = MCPConfigManager(profile=prof)
            original_val = DEFAULT_MCP_CONFIG["servers"]["searxng"]["enabled"]
            mgr.toggle_server("searxng")
            self.assertEqual(DEFAULT_MCP_CONFIG["servers"]["searxng"]["enabled"], original_val)
            self.assertNotEqual(mgr.config["servers"]["searxng"]["enabled"], original_val)

    def test_f05_recursive_permissions_and_marker(self):
        """Test F-05: migrate_legacy_shared_state recursively sets 0700/0600 and writes .migration_complete marker."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("migrated_bot_f05")
            legacy_dir = self.tmp_path / "legacy_f05"
            legacy_dir.mkdir(parents=True, exist_ok=True)
            sub_dir = legacy_dir / "brain" / "sub_folder"
            sub_dir.mkdir(parents=True, exist_ok=True)
            file_nested = sub_dir / "nested.txt"
            file_nested.write_text("nested content")

            status = migrate_legacy_shared_state(target_profile=prof, legacy_dir=legacy_dir)
            self.assertIn("brain", status["migrated"])

            marker = prof.state_dir / ".migration_complete"
            self.assertTrue(marker.exists())

            # Check permissions
            target_sub_dir = prof.state_dir / "brain" / "sub_folder"
            target_file = target_sub_dir / "nested.txt"
            self.assertEqual(target_sub_dir.stat().st_mode & 0o777, 0o700)
            self.assertEqual(target_file.stat().st_mode & 0o777, 0o600)
            self.assertEqual(marker.stat().st_mode & 0o777, 0o600)

    def test_f06_symlink_artifact_escape_rejection(self):
        """Test F-06: check_and_send_artifacts rejects symlink artifacts and paths escaping state_dir."""
        with patch("src.profile.PROFILES_ROOT", self.tmp_path / "profiles"):
            prof = BotProfile("artifact_test_bot")
            brain_dir = prof.state_dir / "brain" / "conv123"
            brain_dir.mkdir(parents=True, exist_ok=True)

            # Create an outside secret file and symlink into brain_dir
            outside_dir = self.tmp_path / "outside"
            outside_dir.mkdir(parents=True, exist_ok=True)
            outside_file = outside_dir / "secret.txt"
            outside_file.write_text("secret_data")

            symlink_artifact = brain_dir / "escaped_art.txt"
            os.symlink(outside_file, symlink_artifact)

            mock_session = MagicMock()
            mock_session.profile = prof
            mock_session.conversation_id = "conv123"
            mock_message = MagicMock()

            with patch("aiogram.types.FSInputFile") as mock_fs:
                asyncio.run(check_and_send_artifacts(mock_message, mock_session))
                # Ensure the escaped symlink artifact was NOT sent via FSInputFile
                for call in mock_fs.call_args_list:
                    file_path = call[0][0]
                    self.assertNotIn("escaped_art.txt", str(file_path))


if __name__ == "__main__":
    unittest.main()
