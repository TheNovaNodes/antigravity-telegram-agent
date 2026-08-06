import unittest
import time
from unittest.mock import patch, MagicMock
from src.session_manager import SessionManager


class TestSessionManager(unittest.TestCase):
    @patch("src.session_manager.save_user_session")
    @patch("src.session_manager.load_user_session", return_value=None)
    def test_get_session_new(self, mock_load, mock_save):
        sm = SessionManager()
        session = sm.get_session(1111)

        self.assertIsNotNone(session)
        self.assertEqual(session.chat_id, 1111)
        self.assertIn(1111, sm.sessions)
        mock_save.assert_called_once()

    @patch("src.session_manager.delete_user_session")
    def test_reset_session(self, mock_delete):
        sm = SessionManager()
        session = sm.get_session(2222)
        mock_child = MagicMock()
        session.child = mock_child

        res = sm.reset_session(2222)
        self.assertTrue(res)
        self.assertNotIn(2222, sm.sessions)
        mock_delete.assert_called_once_with(2222)

    @patch("src.session_manager.delete_user_session")
    def test_cleanup_idle_sessions(self, mock_delete):
        sm = SessionManager(idle_ttl_seconds=10)
        session = sm.get_session(3333)
        mock_child = MagicMock()
        session.child = mock_child

        # Simulate idle time past TTL
        sm.last_accessed[3333] = time.time() - 20
        sm.cleanup_idle_sessions()

        self.assertNotIn(3333, sm.sessions)


if __name__ == "__main__":
    unittest.main()
