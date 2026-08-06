import unittest
from unittest.mock import patch
import os

from src.config import ALLOWED_USER_IDS, BOT_TOKEN, AGY_BINARY_PATH
from src.handlers import is_allowed


class TestConfig(unittest.TestCase):
    def test_config_defaults(self):
        self.assertIsNotNone(BOT_TOKEN)
        self.assertIsNotNone(AGY_BINARY_PATH)
        self.assertIsInstance(ALLOWED_USER_IDS, set)

    def test_is_allowed(self):
        from src.handlers import is_allowed
        # Test with configured user id from .env
        test_id = list(ALLOWED_USER_IDS)[0] if ALLOWED_USER_IDS else 173681771
        self.assertTrue(is_allowed(test_id))
        self.assertFalse(is_allowed(999999999))


if __name__ == "__main__":
    unittest.main()
