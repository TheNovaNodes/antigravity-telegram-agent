import unittest
import asyncio
from unittest.mock import AsyncMock, patch, MagicMock
from pathlib import Path
from src.handlers import check_and_send_artifacts

class TestArtifactDelivery(unittest.TestCase):
    @patch("src.handlers.get_latest_conversation_id")
    def test_check_and_send_artifacts(self, mock_get_latest_id):
        mock_get_latest_id.return_value = "test-conv-123"
        
        message = AsyncMock()
        session = MagicMock()
        session.conversation_id = "test-conv-123"
        
        mock_file = MagicMock()
        mock_file.is_file.return_value = True
        mock_file.name = "report_artifact.md"
        mock_file.stat.return_value.st_mtime = 1000000000.0
        
        with patch.object(Path, "exists", return_value=True), \
             patch.object(Path, "is_dir", return_value=True), \
             patch.object(Path, "iterdir", return_value=[mock_file]), \
             patch("src.handlers.time.time", return_value=1000000010.0), \
             patch("src.handlers.FSInputFile") as mock_fs_input:
            
            asyncio.run(check_and_send_artifacts(message, session))
            message.answer_document.assert_called_once()

if __name__ == "__main__":
    unittest.main()
