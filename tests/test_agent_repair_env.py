import unittest
from src.formatters import highlight_tech_terms

class TestDotEnvHighlight(unittest.TestCase):
    def test_highlight_dot_env(self):
        text = "Check the .env file and .gitignore for details."
        res = highlight_tech_terms(text)
        self.assertIn("<code>.env</code>", res)
        self.assertIn("<code>.gitignore</code>", res)
