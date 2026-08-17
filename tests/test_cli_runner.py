import unittest
from unittest.mock import patch, MagicMock
from src.cli_runner import AgySession, AVAILABLE_MODELS, AVAILABLE_EFFORTS, AVAILABLE_MODES


class TestCliRunner(unittest.TestCase):
    def test_available_constants(self):
        self.assertIn("gemini-flash-high", AVAILABLE_MODELS)
        self.assertIn("high", AVAILABLE_EFFORTS)
        self.assertIn("default", AVAILABLE_MODES)

    def test_agy_session_init(self):
        session = AgySession(12345, model_name="claude-sonnet-4-6", effort="medium", mode="plan")
        self.assertEqual(session.chat_id, 12345)
        self.assertEqual(session.model_name, "claude-sonnet-4-6")
        self.assertEqual(session.effort, "medium")
        self.assertEqual(session.mode, "plan")

    @patch("src.cli_runner.save_user_session")
    def test_set_model(self, mock_save):
        session = AgySession(12345, effort="high")
        
        # Test valid alias
        res = session.set_model("claude-sonnet")
        self.assertTrue(res)
        self.assertEqual(session.model_name, "claude-sonnet-4-6")
        mock_save.assert_called_with(12345, "claude-sonnet-4-6", "high", "default", None, None)

        # Test invalid alias
        res_invalid = session.set_model("non-existent-model")
        self.assertFalse(res_invalid)

    @patch("src.cli_runner.save_user_session")
    def test_set_effort(self, mock_save):
        session = AgySession(12345)
        res = session.set_effort("low")
        self.assertTrue(res)
        self.assertEqual(session.effort, "low")

        res_invalid = session.set_effort("ultra-mega")
        self.assertFalse(res_invalid)

    @patch("src.cli_runner.save_user_session")
    def test_set_mode(self, mock_save):
        session = AgySession(12345)
        res = session.set_mode("plan")
        self.assertTrue(res)
        self.assertEqual(session.mode, "plan")

        res_invalid = session.set_mode("unknown-mode")
        self.assertFalse(res_invalid)

    def test_close_session(self):
        session = AgySession(12345)
        mock_child = MagicMock()
        mock_child.isalive.return_value = True
        session.child = mock_child

        session.close()
        mock_child.close.assert_called_once_with(force=True)
        self.assertIsNone(session.child)

    def test_close_session_with_exception(self):
        session = AgySession(12345)
        mock_child = MagicMock()
        mock_child.isalive.return_value = True
        mock_child.close.side_effect = Exception("Process exit error")
        session.child = mock_child

        session.close()
        self.assertIsNone(session.child)

    @patch("src.cli_runner.AgySession._ensure_started")
    @patch("src.cli_runner.pyte")
    def test_get_response_handles_send_error_and_restarts(self, mock_pyte, mock_ensure_started):
        import asyncio
        import pexpect

        session = AgySession(12345)
        mock_child1 = MagicMock()
        mock_child2 = MagicMock()
        mock_child2.isalive.return_value = True

        def send_side_effect(data):
            if session.child is mock_child1:
                session.close()
                raise pexpect.EOF("Process dead")
            return None

        mock_child1.send.side_effect = send_side_effect
        mock_child2.send.return_value = None
        # EOF exits the read loop immediately (vs TIMEOUT which takes 300 iterations)
        mock_child2.read_nonblocking.side_effect = pexpect.EOF("done")

        async def fake_ensure():
            if session.child is None:
                session.child = mock_child2

        mock_ensure_started.side_effect = fake_ensure
        session.child = mock_child1

        mock_screen = MagicMock()
        mock_screen.display = ["Response line 1"]
        mock_pyte.Screen.return_value = mock_screen

        response = asyncio.run(session.get_response("hello"))
        self.assertEqual(mock_child1.send.call_count, 1)
        self.assertEqual(mock_child2.send.call_count, 1)

    @patch("src.cli_runner.AgySession._ensure_started")
    @patch("src.cli_runner.pyte")
    def test_soul_bootstrap_injection(self, mock_pyte, mock_ensure_started):
        import asyncio
        import pexpect

        session = AgySession(12345)
        mock_child = MagicMock()
        mock_child.isalive.return_value = True
        mock_child.read_nonblocking.side_effect = pexpect.EOF("done")

        async def fake_ensure():
            session.child = mock_child

        mock_ensure_started.side_effect = fake_ensure
        mock_screen = MagicMock()
        mock_screen.display = ["Ready"]
        mock_pyte.Screen.return_value = mock_screen

        # Consume generator
        async def run_stream():
            chunks = []
            async for chunk in session.stream_response("Tell me a story"):
                chunks.append(chunk)
            return chunks

        asyncio.run(run_stream())
        self.assertTrue(mock_child.send.called)
        sent_bytes = mock_child.send.call_args[0][0]
        sent_text = sent_bytes.decode("utf-8")
        self.assertIn("SYSTEM INSTRUCTION / SOUL BOOTSTRAP", sent_text)
        self.assertIn("ТРИКСТЕР", sent_text)


if __name__ == "__main__":
    unittest.main()




