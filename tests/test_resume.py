import unittest
from unittest.mock import patch, MagicMock
from src.conversations import get_available_conversations
from src.cli_runner import AgySession


class TestResume(unittest.TestCase):
    def test_get_available_conversations(self):
        conversations = get_available_conversations(limit=5)
        self.assertIsInstance(conversations, list)
        if conversations:
            first = conversations[0]
            self.assertIn("id", first)
            self.assertIn("summary", first)
            self.assertIn("step_count", first)

    @patch("src.cli_runner.save_user_session")
    def test_session_set_conversation(self, mock_save):
        session = AgySession(chat_id=12345)
        res = session.set_conversation("41db2852-7d89-41f5-9ab9-6b1d6c26c07d")
        self.assertTrue(res)
        self.assertEqual(session.conversation_id, "41db2852-7d89-41f5-9ab9-6b1d6c26c07d")
        mock_save.assert_called_with(12345, "gemini-3.1-pro-high", "high", "default", "41db2852-7d89-41f5-9ab9-6b1d6c26c07d", None)

    @patch("src.cli_runner.pexpect.spawn")
    def test_ensure_started_includes_conversation_flag(self, mock_spawn):
        import pexpect
        mock_child = MagicMock()
        mock_child.isalive.return_value = True
        mock_child.read_nonblocking.side_effect = pexpect.TIMEOUT("timeout")
        mock_spawn.return_value = mock_child

        session = AgySession(chat_id=12345, conversation_id="test-conv-uuid")
        import asyncio
        asyncio.run(session._ensure_started())

        # Verify --conversation test-conv-uuid was passed in args
        spawn_args = mock_spawn.call_args[0][1]
        self.assertIn("--conversation", spawn_args)
        self.assertIn("test-conv-uuid", spawn_args)


if __name__ == "__main__":
    unittest.main()
