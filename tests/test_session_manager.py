import unittest
import time
from unittest.mock import patch, MagicMock
from src.session_manager import SessionManager
from src.session_key import SessionKey


class TestSessionManager(unittest.TestCase):
    @patch("src.session_manager.save_user_session")
    @patch("src.session_manager.load_user_session", return_value=None)
    def test_get_session_new(self, mock_load, mock_save):
        sm = SessionManager()
        session = sm.get_session(1111)
        sk = SessionKey(0, 1111)

        self.assertIsNotNone(session)
        self.assertEqual(session.chat_id, 1111)
        self.assertIn(sk, sm.sessions)
        mock_save.assert_called_once()

    @patch("src.session_manager.delete_user_session")
    def test_reset_session(self, mock_delete):
        sm = SessionManager()
        session = sm.get_session(2222)
        sk = SessionKey(0, 2222)
        mock_child = MagicMock()
        session.child = mock_child

        res = sm.reset_session(2222)
        self.assertTrue(res)
        self.assertNotIn(sk, sm.sessions)
        mock_delete.assert_called_once_with(sk)

    @patch("src.session_manager.delete_user_session")
    def test_cleanup_idle_sessions(self, mock_delete):
        sm = SessionManager(idle_ttl_seconds=10)
        session = sm.get_session(3333)
        sk = SessionKey(0, 3333)
        mock_child = MagicMock()
        session.child = mock_child

        # Simulate idle time past TTL
        sm.last_accessed[sk] = time.time() - 20
        sm.cleanup_idle_sessions()

        self.assertNotIn(sk, sm.sessions)


if __name__ == "__main__":
    unittest.main()
