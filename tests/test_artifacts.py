import unittest
import asyncio
import time
import tempfile
import os
from unittest.mock import AsyncMock, patch, MagicMock
from pathlib import Path
from src.handlers import check_and_send_artifacts


class TestArtifactDelivery(unittest.TestCase):
    def test_check_and_send_artifacts_delivers_recent_files(self):
        """Test that recently modified artifact files are detected and sent."""
        with tempfile.TemporaryDirectory() as tmpdir:
            home_dir = Path(tmpdir) / "home_mock"
            home_dir.mkdir(exist_ok=True)
            gemini_dir = home_dir / ".gemini" / "antigravity-cli" / "brain"
            gemini_dir.mkdir(parents=True, exist_ok=True)

            mock_conv_dir = gemini_dir / "test-conv-123"
            mock_conv_dir.mkdir()
            artifact2 = mock_conv_dir / "analysis.md"
            artifact2.write_text("# Analysis Result")

            message = AsyncMock()
            session = MagicMock()
            session.conversation_id = "test-conv-123"
            session.profile = None

            with patch("src.handlers.Path.home", return_value=home_dir), \
                 patch("src.handlers.FSInputFile") as mock_fs_input:
                asyncio.run(check_and_send_artifacts(message, session))
                message.answer_document.assert_called_once()
                call_kwargs = message.answer_document.call_args
                self.assertIn("Session Artifact", call_kwargs.kwargs.get("caption", ""))

    def test_check_and_send_artifacts_skips_system_dirs(self):
        """Test that artifacts inside .system_generated, scratch, .user_uploaded are skipped."""
        with tempfile.TemporaryDirectory() as tmpdir:
            home_dir = Path(tmpdir)
            brain_base = home_dir / ".gemini" / "antigravity-cli" / "brain"
            conv_dir = brain_base / "test-conv-456"
            conv_dir.mkdir(parents=True)
            
            # Only create system files — no real artifacts
            for sys_name in [".system_generated", "scratch", ".user_uploaded"]:
                d = conv_dir / sys_name
                d.mkdir()
                f = d / "should_skip.md"
                f.write_text("skip me")
            
            message = AsyncMock()
            session = MagicMock()
            session.conversation_id = "test-conv-456"
            session.profile = None
            
            with patch("src.handlers.Path.home", return_value=home_dir), \
                 patch("src.handlers.FSInputFile"):
                asyncio.run(check_and_send_artifacts(message, session))
                message.answer_document.assert_not_called()

    def test_check_and_send_artifacts_no_brain_dir(self):
        """Test graceful handling when brain directory does not exist."""
        with tempfile.TemporaryDirectory() as tmpdir:
            home_dir = Path(tmpdir) / "nonexistent_home"
            home_dir.mkdir()
            
            message = AsyncMock()
            session = MagicMock()
            session.conversation_id = "some-conv-id"
            session.profile = None
            
            with patch("src.handlers.Path.home", return_value=home_dir):
                asyncio.run(check_and_send_artifacts(message, session))
                message.answer_document.assert_not_called()

    def test_check_and_send_artifacts_deduplicates_by_filename(self):
        """Test that same-name artifacts from different strategies are deduplicated."""
        with tempfile.TemporaryDirectory() as tmpdir:
            home_dir = Path(tmpdir)
            brain_base = home_dir / ".gemini" / "antigravity-cli" / "brain"
            
            # Create two conv dirs, both with an artifact named "report.md"
            for conv_id in ["conv-1", "conv-2"]:
                d = brain_base / conv_id
                d.mkdir(parents=True)
                f = d / "report.md"
                f.write_text(f"# Report from {conv_id}")
            
            message = AsyncMock()
            session = MagicMock()
            session.conversation_id = "conv-1"
            session.profile = None
            
            with patch("src.handlers.Path.home", return_value=home_dir), \
                 patch("src.handlers.get_latest_conversation_id", return_value="conv-2"), \
                 patch("src.handlers.FSInputFile"):
                asyncio.run(check_and_send_artifacts(message, session))
                # Should only send once despite two dirs having report.md
                self.assertEqual(message.answer_document.call_count, 1)


if __name__ == "__main__":
    unittest.main()
