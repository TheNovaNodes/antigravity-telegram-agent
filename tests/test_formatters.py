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
        self.assertFalse(is_tui_noise("Это обычный текст ответа от модели."))
        self.assertFalse(is_tui_noise("| Параметр | Значение |"))

    def test_markdown_tables_formatting(self):
        table_md = (
            "Вот таблица портов:\n\n"
            "| Порт | Сервис |\n"
            "| --- | --- |\n"
            "| 3002 | AnythingLLM |\n"
            "| 8889 | SearXNG |\n\n"
            "Конец таблицы."
        )
        html = markdown_to_html(table_md)
        self.assertIn("<pre><code>| Порт | Сервис |\n| --- | --- |\n| 3002 | AnythingLLM |\n| 8889 | SearXNG |</code></pre>", html)

    def test_check_known_errors_eligibility(self):
        sample_error = "Eligibility Check\nEligibility check failed: Your current account is not eligible for Antigravity, because it is not currently available in your location."
        result = check_known_errors(sample_error)
        self.assertIsNotNone(result)
        self.assertIn("Ошибка доступа к аккаунту", result)
        self.assertIn("<code>agy auth login</code>", result)

    def test_markdown_to_html_formatting(self):
        md_text = (
            "# Главный заголовок\n\n"
            "Вот **жирный текст** и *курсив*, а также `код`.\n"
            "> Это важная цитата\n\n"
            "```python\ndef hello():\n    return 'world'\n```\n"
            "Ссылка: [Google](https://google.com)\n"
            "Путь к файлу: src/db.py"
        )
        html = markdown_to_html(md_text)
        self.assertIn("<b><u>Главный заголовок</u></b>", html)
        self.assertIn("<b>жирный текст</b>", html)
        self.assertIn("<i>курсив</i>", html)
        self.assertIn("<code>код</code>", html)
        self.assertIn("<blockquote>Это важная цитата</blockquote>", html)
        self.assertIn("def hello():", html)
        self.assertIn('<a href="https://google.com">Google</a>', html)
        self.assertIn("<code>src/db.py</code>", html)

    def test_highlight_tech_terms(self):
        text = "Проверьте файл src/handlers.py или config.json для подробностей."
        highlighted = highlight_tech_terms(text)
        self.assertIn("<code>src/handlers.py</code>", highlighted)
        self.assertIn("<code>config.json</code>", highlighted)

    def test_format_dyslexia_friendly_text_unwrapping(self):
        raw_screen = [
            "      ▄▀▀▄        Antigravity CLI 1.1.10",
            "    ▀▀▀▀▀▀▀▀      Gemini 3.6 Flash (Low)",
            "   ▄▀▀    ▀▀▄     ~/LabDoctorM/projects/antigravity-telegram-agent:",
            "  ▄▀▀      ▀▀▄",
            "> Расскажи о космосе",
            "Привет! Вот 3 интересных факта о космосе, о которых вы могли не",
            "знать. Первое: в космосе царит абсолютная тишина.",
            "",
            "Второе: количество звезд во Вселенной больше, чем песчинок на",
            "всех пляжах Земли."
        ]

        formatted = format_dyslexia_friendly_text(raw_screen, prompt="Расскажи о космосе")
        
        self.assertNotIn("Antigravity CLI", formatted)
        self.assertNotIn("Gemini 3.6", formatted)
        self.assertNotIn("▄▀▀", formatted)
        self.assertNotIn("Расскажи о космосе", formatted)
        self.assertIn("о которых вы могли не знать.", formatted)
        self.assertIn("\n\n", formatted)


if __name__ == "__main__":
    unittest.main()
