import unittest
from unittest.mock import AsyncMock, MagicMock
import asyncio

from src.handlers import send_response_chunks


class TestChunking(unittest.TestCase):
    def test_single_short_chunk(self):
        message = AsyncMock()
        placeholder = AsyncMock()
        text = "Hello world short response"

        asyncio.run(send_response_chunks(message, placeholder, text))

        placeholder.edit_text.assert_called_once_with(text, parse_mode="HTML")
        message.answer.assert_not_called()

    def test_long_response_splitting(self):
        message = AsyncMock()
        placeholder = AsyncMock()

        # Create a text between 3801 and 8000 chars
        paragraph1 = "A" * 3500
        paragraph2 = "B" * 2000
        text = f"{paragraph1}\n{paragraph2}"

        asyncio.run(send_response_chunks(message, placeholder, text))

        placeholder.edit_text.assert_called_once_with(paragraph1, parse_mode="HTML")
        message.answer.assert_called_once_with(paragraph2, parse_mode="HTML")

    def test_huge_response_document_attachment(self):
        message = AsyncMock()
        placeholder = AsyncMock()

        # Create text > 8000 chars
        text = "X" * 10000

        asyncio.run(send_response_chunks(message, placeholder, text))

        # Check placeholder edit contains notice
        placeholder.edit_text.assert_called_once()
        self.assertIn("[Ответ слишком большой. Полная версия в файле ниже]", placeholder.edit_text.call_args[0][0])

        # Check document attachment was sent
        message.answer_document.assert_called_once()
        self.assertIn("10000 символов", message.answer_document.call_args[1]["caption"])


if __name__ == "__main__":
    unittest.main()

