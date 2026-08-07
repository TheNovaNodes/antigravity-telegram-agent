import unittest
from src.formatters import is_tui_noise, check_known_errors, format_dyslexia_friendly_text


class TestFormatters(unittest.TestCase):
    def test_is_tui_noise(self):
        self.assertTrue(is_tui_noise("   Antigravity CLI 1.1.10   "))
        self.assertTrue(is_tui_noise("Gemini 3.6 Flash (Low)"))
        self.assertTrue(is_tui_noise("> my prompt text"))
        self.assertTrue(is_tui_noise("─────"))
        self.assertTrue(is_tui_noise("   ▄▀▀ ▀▀▄   "))
        self.assertFalse(is_tui_noise("Это обычный текст ответа от модели."))

    def test_check_known_errors_eligibility(self):
        sample_error = "Eligibility Check\nEligibility check failed: Your current account is not eligible for Antigravity, because it is not currently available in your location."
        result = check_known_errors(sample_error)
        self.assertIsNotNone(result)
        self.assertIn("Ошибка доступа к аккаунту", result)
        self.assertIn("`agy auth login`", result)

    def test_format_dyslexia_friendly_text_unwrapping(self):
        raw_screen = [
            "      ▄▀▀▄        Antigravity CLI 1.1.10",
            "    ▀▀▀▀▀▀▀▀      Gemini 3.6 Flash (Low)",
            "   ▄▀▀    ▀▀▄     ~/LabDoctorM/projects/DMagyBOT:",
            "  ▄▀▀      ▀▀▄",
            "> Расскажи о космосе",
            "Привет! Вот 3 интересных факта о космосе, о которых вы могли не",
            "знать. Первое: в космосе царит абсолютная тишина.",
            "",
            "Второе: количество звезд во Вселенной больше, чем песчинок на",
            "всех пляжах Земли."
        ]

        formatted = format_dyslexia_friendly_text(raw_screen)
        
        # Verify no ASCII banner art or CLI headers
        self.assertNotIn("Antigravity CLI", formatted)
        self.assertNotIn("Gemini 3.6", formatted)
        self.assertNotIn("▄▀▀", formatted)

        # Verify unwrapped natural paragraphs with double newlines
        self.assertIn("о которых вы могли не знать.", formatted)
        self.assertIn("\n\n", formatted)


if __name__ == "__main__":
    unittest.main()
