import unittest
import asyncio
from unittest.mock import AsyncMock, patch, MagicMock

from aiogram.types import Message, CallbackQuery, User, Chat
from src.handlers import (
    cmd_menu, cmd_models, cmd_effort, cmd_mode, cmd_reset,
    process_menu_navigation, process_model_callback, handle_message, is_allowed
)


class TestHandlers(unittest.TestCase):
    def setUp(self):
        self.user = User(id=173681771, is_bot=False, first_name="TestUser")
        self.chat = Chat(id=173681771, type="private")

    @patch("src.handlers.is_allowed", return_value=True)
    def test_cmd_menu_allowed(self, mock_allowed):
        message = AsyncMock(spec=Message)
        message.from_user = self.user
        message.chat = self.chat
        message.answer = AsyncMock()

        asyncio.run(cmd_menu(message))
        message.answer.assert_called_once()

    @patch("src.handlers.is_allowed", return_value=False)
    def test_cmd_menu_denied(self, mock_allowed):
        message = AsyncMock(spec=Message)
        message.from_user = User(id=99999, is_bot=False, first_name="Denied")
        message.chat = self.chat
        message.answer = AsyncMock()

        asyncio.run(cmd_menu(message))
        message.answer.assert_not_called()

    @patch("src.handlers.log_audit_event")
    @patch("src.handlers.session_manager")
    @patch("src.handlers.is_allowed", return_value=True)
    def test_handle_message_success(self, mock_allowed, mock_sm, mock_audit):
        message = AsyncMock(spec=Message)
        message.from_user = self.user
        message.chat = self.chat
        message.text = "Hello agent"
        
        placeholder = AsyncMock()
        message.answer = AsyncMock(return_value=placeholder)
        message.bot = AsyncMock()

        mock_session = AsyncMock()
        mock_session.get_response = AsyncMock(return_value="Agent response text")
        mock_session.model_name = "gemini-3.6-flash-high"
        mock_session.effort = "high"
        mock_session.mode = "default"
        mock_sm.get_session.return_value = mock_session

        asyncio.run(handle_message(message))

        mock_session.get_response.assert_called_once_with("Hello agent")
        placeholder.edit_text.assert_called_once_with("Agent response text", parse_mode="HTML")
        mock_audit.assert_called_once()


if __name__ == "__main__":
    unittest.main()
