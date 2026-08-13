import unittest
from src.formatters import (
    is_tui_noise,
    check_known_errors,
    extract_new_response_lines,
    format_dyslexia_friendly_text,
    markdown_to_html,
    highlight_tech_terms
)


class TestFormatters(unittest.TestCase):
    def test_is_tui_noise(self):
        self.assertTrue(is_tui_noise("   Antigravity CLI 1.1.10   "))
        self.assertTrue(is_tui_noise("Gemini 3.6 Flash (Low)"))
        self.assertTrue(is_tui_noise("> my prompt text"))
        self.assertTrue(is_tui_noise("   ▄▀▀ ▀▀▄   "))
        self.assertFalse(is_tui_noise("This is regular response text from the model."))
        self.assertFalse(is_tui_noise("| Parameter | Value |"))

    def test_markdown_tables_formatting(self):
        table_md = (
            "Here is the port table:\n\n"
            "| Port | Service |\n"
            "| --- | --- |\n"
            "| 3002 | AnythingLLM |\n"
            "| 8889 | SearXNG |\n\n"
            "End of table."
        )
        html = markdown_to_html(table_md)
        self.assertIn("<pre><code>| Port | Service |\n| --- | --- |\n| 3002 | AnythingLLM |\n| 8889 | SearXNG |</code></pre>", html)

    def test_check_known_errors_eligibility(self):
        sample_error = "Eligibility Check\nEligibility check failed: Your current account is not eligible for Antigravity, because it is not currently available in your location."
        result = check_known_errors(sample_error)
        self.assertIsNotNone(result)
        self.assertIn("Account Access Error", result)
        self.assertIn("<code>agy auth login</code>", result)

    def test_markdown_to_html_formatting(self):
        md_text = (
            "# Main header\n\n"
            "Here is **bold text** and *italic*, as well as `code`.\n"
            "> This is an important quote\n\n"
            "```python\ndef hello():\n    return 'world'\n```\n"
            "Link: [Google](https://google.com)\n"
            "File path: src/db.py"
        )
        html = markdown_to_html(md_text)
        self.assertIn("<b><u>Main header</u></b>", html)
        self.assertIn("<b>bold text</b>", html)
        self.assertIn("<i>italic</i>", html)
        self.assertIn("<code>code</code>", html)
        self.assertIn("<blockquote>This is an important quote</blockquote>", html)
        self.assertIn("def hello():", html)
        self.assertIn('<a href="https://google.com">Google</a>', html)
        self.assertIn("<code>src/db.py</code>", html)

    def test_highlight_tech_terms(self):
        text = "Check the file src/handlers.py or config.json for details."
        highlighted = highlight_tech_terms(text)
        self.assertIn("<code>src/handlers.py</code>", highlighted)
        self.assertIn("<code>config.json</code>", highlighted)

    def test_format_dyslexia_friendly_text_unwrapping(self):
        raw_screen = [
            "      ▄▀▀▄        Antigravity CLI 1.1.10",
            "    ▀▀▀▀▀▀▀▀      Gemini 3.6 Flash (Low)",
            "   ▄▀▀    ▀▀▄     ~/LabDoctorM/projects/antigravity-telegram-agent:",
            "  ▄▀▀      ▀▀▄",
            "> Tell me about space",
            "Hello! Here are 3 interesting facts about space that you might not",
            "know. First: space is absolutely silent.",
            "",
            "Second: the number of stars in the Universe is greater than the grains of sand on",
            "all the beaches of Earth."
        ]

        formatted = format_dyslexia_friendly_text(raw_screen, prompt="Tell me about space")
        
        self.assertNotIn("Antigravity CLI", formatted)
        self.assertNotIn("Gemini 3.6", formatted)
        self.assertNotIn("▄▀▀", formatted)
        self.assertNotIn("Tell me about space", formatted)
        self.assertIn("you might not know.", formatted)
        self.assertIn("\n\n", formatted)


if __name__ == "__main__":
    unittest.main()
