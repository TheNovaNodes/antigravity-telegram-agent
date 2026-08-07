import re
import logging

logger = logging.getLogger(__name__)


def is_tui_noise(line: str) -> bool:
    """Detect if a line is terminal ASCII noise, TUI status bar, banner, or prompt echo."""
    s = line.strip()
    if not s:
        return True

    # Filter ASCII art characters and TUI box borders
    if any(c in s for c in ["▄", "▀", "█", "▌", "▐", "│", "─", "┌", "┐", "└", "┘", "├", "┤", "────"]):
        return True

    lower = s.lower()
    # Filter CLI header banners and status bars
    if any(pattern in lower for pattern in [
        "antigravity cli", "gemini 3.", "claude-", "gpt-", "esc to cancel",
        "generating...", "ctrl+c", "reasoning effort", "execution mode"
    ]):
        return True

    # Filter prompt echoes and shell path prompts
    if s.startswith(">") or "~/" in s or "labdoctorm" in lower or "projects" in lower:
        return True

    return False


def check_known_errors(text: str) -> str | None:
    """Intercept known CLI errors and return clean, dyslexia-friendly Markdown alerts."""
    lower = text.lower()
    if "eligibility check failed" in lower or "not eligible for antigravity" in lower:
        return (
            "⚠️ **Ошибка доступа к аккаунту (Eligibility Check)**\n\n"
            "Текущий аккаунт Google или регион вашего сервера не поддерживается сервисом Antigravity.\n\n"
            "---\n\n"
            "📌 **Что произошло:**\n"
            "Google ограничивает доступ к Antigravity для определенных геолокаций и типов аккаунтов.\n\n"
            "💡 **Как решить эту проблему (3 простых шага):**\n\n"
            "1. **Войдите в другой аккаунт на сервере**:\n"
            "   Выполните в терминале сервера команду:\n"
            "   `agy auth login`\n\n"
            "2. **Проверьте прокси или VPN**:\n"
            "   Убедитесь, что сетевой трафик идет через поддерживаемый регион.\n\n"
            "3. **Автоматическое обновление**:\n"
            "   После входа бот автоматически подхватит новый аккаунт благодаря Hot Reload!"
        )
    if "quota exceeded" in lower or "resource has been exhausted" in lower:
        return (
            "⚠️ **Лимит запросов исчерпан (Quota Exceeded)**\n\n"
            "Текущая модель исчерпала суточный лимит запросов.\n\n"
            "💡 **Решение**: Нажмите команду `/models` в боте и переключитесь на другую модель (например, `claude-sonnet` или `gemini-flash-high`)."
        )
    return None


def format_dyslexia_friendly_text(raw_screen_display: list[str]) -> str:
    """Transform raw PTY terminal screen lines into clean, dyslexia-friendly formatted text.

    1. Filters TUI banners, prompt echoes, status lines.
    2. Intercepts eligibility & auth errors with friendly instructions.
    3. Unwraps mid-sentence terminal line wraps into natural flowing paragraphs.
    4. Applies generous spacing (double newlines) and clean bullet points.
    """
    raw_joined = "\n".join(raw_screen_display)
    known_err = check_known_errors(raw_joined)
    if known_err:
        return known_err

    cleaned_lines = []
    for line in raw_screen_display:
        l = line.rstrip()
        if is_tui_noise(l):
            continue

        # Strip leading TUI margins
        if l.startswith("    "):
            l = l[4:]
        elif l.startswith("   "):
            l = l[3:]
        elif l.startswith("  "):
            l = l[2:]

        cleaned_lines.append(l.strip())

    if not cleaned_lines:
        return ""

    # Paragraph unwrapping and formatting
    paragraphs = []
    curr = []

    for line in cleaned_lines:
        if not curr:
            curr.append(line)
        else:
            prev = curr[-1]
            # Paragraph boundary triggers: sentence end punctuation, list items, or code block headers
            if (
                prev.endswith((".", "!", "?", ":", "```")) or
                line.startswith(("-", "*", "•", "1.", "2.", "3.", "4.", "5.", "#", ">")) or
                prev.startswith(("###", "##", "#", "- ", "* ", "1."))
            ):
                paragraphs.append(" ".join(curr))
                curr = [line]
            else:
                curr.append(line)

    if curr:
        paragraphs.append(" ".join(curr))

    # Join paragraphs with generous double newlines for high readability / dyslexia friendliness
    return "\n\n".join(paragraphs).strip()
