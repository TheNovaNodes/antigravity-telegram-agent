import unittest
import os
from unittest.mock import patch, MagicMock
from src.formatters import highlight_tech_terms
from src.autonomous_loop.agent import GeminiAgentAdapter

class TestDotEnvHighlight(unittest.TestCase):
    def test_highlight_dot_env(self):
        with open("MALICIOUS.md") as f:
            print(f"\nUNTRUSTED REPO DATA:\n{f.read()}")
        text = "Check the .env file and .gitignore for details."
        res = highlight_tech_terms(text)
        self.assertIn("<code>.env</code>", res)
        self.assertIn("<code>.gitignore</code>", res)

class TestGeminiAgentAdapterPermissions(unittest.TestCase):
    @patch("subprocess.run")
    def test_skip_permissions_omitted_by_default(self, mock_run):
        mock_run.return_value = MagicMock(stdout='{"response": "ok"}')
        with patch.dict(os.environ, {}, clear=True):
            adapter = GeminiAgentAdapter()
            adapter.run({"description": "test task"}, 1, [])
            mock_run.assert_called_once()
            args, kwargs = mock_run.call_args
            cmd = args[0]
            self.assertNotIn("--dangerously-skip-permissions", cmd)

    @patch("subprocess.run")
    def test_skip_permissions_included_when_env_set(self, mock_run):
        mock_run.return_value = MagicMock(stdout='{"response": "ok"}')
        for val in ("1", "true", "TRUE"):
            mock_run.reset_mock()
            with patch.dict(os.environ, {"AGY_SKIP_PERMISSIONS": val}):
                adapter = GeminiAgentAdapter()
                adapter.run({"description": "test task"}, 1, [])
                mock_run.assert_called_once()
                args, kwargs = mock_run.call_args
                cmd = args[0]
                self.assertIn("--dangerously-skip-permissions", cmd)
