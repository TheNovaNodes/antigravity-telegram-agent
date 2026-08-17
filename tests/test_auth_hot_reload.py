import unittest
from unittest.mock import patch, MagicMock
from pathlib import Path
from tempfile import TemporaryDirectory

from src.cli_runner import AgySession, get_auth_state_signature


class TestAuthHotReload(unittest.TestCase):
    def test_get_auth_state_signature_returns_string(self):
        sig = get_auth_state_signature()
        self.assertIsInstance(sig, str)
        self.assertIn("token:", sig)

    @patch("src.cli_runner.get_auth_state_signature")
    def test_hot_reload_triggers_on_credential_change(self, mock_sig):
        mock_sig.return_value = "token:v1"

        session = AgySession(chat_id=12345)
        session.close = MagicMock()

        # Simulate initial process spawned with auth signature v1
        mock_child = MagicMock()
        mock_child.isalive.return_value = True
        session.child = mock_child
        session.spawn_auth_signature = "token:v1"

        # Now simulate account switch on host server (new signature v2)
        mock_sig.return_value = "token:v2"

        # Call get_auth_state_signature check
        import asyncio
        with patch("src.cli_runner.pexpect.spawn") as mock_spawn:
            mock_new_child = MagicMock()
            mock_new_child.isalive.return_value = True
            mock_new_child.read_nonblocking.return_value = "> "
            mock_spawn.return_value = mock_new_child
            asyncio.run(session._ensure_started())

        # Assert session.close() was called to terminate old stale PTY process
        session.close.assert_called_once()


if __name__ == "__main__":
    unittest.main()
