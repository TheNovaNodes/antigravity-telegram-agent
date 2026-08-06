import unittest
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

from src.audit import log_audit_event


class TestAudit(unittest.TestCase):
    def test_log_audit_event(self):
        with TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / "audit.log"
            with patch("src.audit.AUDIT_LOG_PATH", temp_path), \
                 patch("src.audit.LOGS_DIR", Path(temp_dir)):
                
                log_audit_event(
                    user_id=12345,
                    chat_id=67890,
                    model_name="gemini-3.6-flash-high",
                    effort="high",
                    mode="default",
                    prompt="Hello test prompt",
                    response_length=150
                )

                self.assertTrue(temp_path.exists())
                lines = temp_path.read_text(encoding="utf-8").strip().split("\n")
                self.assertEqual(len(lines), 1)

                entry = json.loads(lines[0])
                self.assertEqual(entry["user_id"], 12345)
                self.assertEqual(entry["chat_id"], 67890)
                self.assertEqual(entry["model_name"], "gemini-3.6-flash-high")
                self.assertEqual(entry["prompt"], "Hello test prompt")
                self.assertEqual(entry["response_length"], 150)


if __name__ == "__main__":
    unittest.main()
