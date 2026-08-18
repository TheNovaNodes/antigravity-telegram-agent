import unittest
import asyncio
from unittest.mock import AsyncMock, patch, MagicMock
from aiogram.types import CallbackQuery, User, Chat, Message
from src.jules_monitor import PENDING_JULES_PATCHES, make_session_hash
from src.handlers import process_jules_test_callback

class TestJulesPatchTrigger(unittest.TestCase):
    def setUp(self):
        self.user = User(id=173681771, is_bot=False, first_name="TestUser")
        self.chat = Chat(id=173681771, type="private")

    def test_session_hash(self):
        sess_name = "projects/123/locations/global/sessions/456"
        h = make_session_hash(sess_name)
        self.assertEqual(len(h), 12)

    @patch("src.handlers.is_allowed", return_value=True)
    @patch("src.handlers.session_manager")
    @patch("asyncio.create_subprocess_exec")
    def test_process_jules_test_callback_success(self, mock_subproc, mock_sm, mock_allowed):
        sess_name = "test_session_123"
        sess_hash = make_session_hash(sess_name)
        PENDING_JULES_PATCHES[sess_hash] = {
            "session_name": sess_name,
            "patch_text": "--- a/file.py\n+++ b/file.py\n@@ -1 +1 @@\n-old\n+new"
        }

        callback_query = AsyncMock(spec=CallbackQuery)
        callback_query.from_user = self.user
        callback_query.data = f"jules_test:{sess_hash}"
        callback_query.answer = AsyncMock()
        
        status_msg = AsyncMock(spec=Message)
        status_msg.edit_text = AsyncMock()
        callback_query.message = AsyncMock(spec=Message)
        callback_query.message.chat = self.chat
        callback_query.message.reply = AsyncMock(return_value=status_msg)

        mock_session = MagicMock()
        mock_session.workspace = "/root/projects/TheNovaNodes/antigravity-telegram-agent"
        mock_sm.get_session.return_value = mock_session

        # Mock git apply subproc
        apply_proc = AsyncMock()
        apply_proc.returncode = 0
        apply_proc.communicate.return_value = (b"", b"")

        # Mock pytest subproc
        test_proc = AsyncMock()
        test_proc.returncode = 0
        test_proc.communicate.return_value = (b"=== 1 passed in 0.1s ===", b"")

        mock_subproc.side_effect = [apply_proc, test_proc]

        asyncio.run(process_jules_test_callback(callback_query))

        callback_query.answer.assert_called()
        callback_query.message.reply.assert_called_once()
        self.assertEqual(mock_subproc.call_count, 2)


if __name__ == "__main__":
    unittest.main()
