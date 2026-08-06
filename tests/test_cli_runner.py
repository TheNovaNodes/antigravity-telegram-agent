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
        session = AgySession(12345)
        
        # Test valid alias
        res = session.set_model("claude-sonnet")
        self.assertTrue(res)
        self.assertEqual(session.model_name, "claude-sonnet-4-6")
        mock_save.assert_called_with(12345, "claude-sonnet-4-6", "high", "default")

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


if __name__ == "__main__":
    unittest.main()
