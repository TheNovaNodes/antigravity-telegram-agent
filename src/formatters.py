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
    # Filter CLI header banners, status bars, and reasoning traces
    if any(pattern in lower for pattern in [
        "antigravity cli", "gemini 3.", "claude-", "gpt-", "esc to cancel",
        "generating...", "ctrl+c", "reasoning effort", "execution mode",
        "thought for", "prioritizing tool", "tool usage"
    ]):
        return True

    # Filter prompt echoes and shell path prompts
    if s.startswith(">") or "~/" in s or "labdoctorm" in lower or "projects" in lower:
        return True

    return False


def check_known_errors(text: str) -> str | None:
    """Intercept known CLI errors and return clean, dyslexia-friendly HTML alerts."""
    lower = text.lower()
    if "eligibility check failed" in lower or "not eligible for antigravity" in lower:
        return (
            "⚠️ <b>Ошибка доступа к аккаунту (Eligibility Check)</b>\n\n"
            "Текущий аккаунт Google или регион вашего сервера не поддерживается сервисом Antigravity.\n\n"
            "───────────────\n\n"
            "📌 <b>Что произошло:</b>\n"
            "Google ограничивает доступ к Antigravity для определенных геолокаций и типов аккаунтов.\n\n"
            "💡 <b>Как решить эту проблему (3 простых шага):</b>\n\n"
            "1. <b>Войдите в другой аккаунт на сервере</b>:\n"
            "   Выполните в терминале сервера команду:\n"
            "   <code>agy auth login</code>\n\n"
            "2. <b>Проверьте прокси или VPN</b>:\n"
            "   Убедитесь, что сетевой трафик идет через поддерживаемый регион.\n\n"
            "3. <b>Автоматическое обновление</b>:\n"
            "   После входа бот автоматически подхватит новый аккаунт благодаря Hot Reload!"
        )
    if "quota exceeded" in lower or "resource has been exhausted" in lower:
        return (
            "⚠️ <b>Лимит запросов исчерпан (Quota Exceeded)</b>\n\n"
            "Текущая модель исчерпала суточный лимит запросов.\n\n"
            "💡 <b>Решение</b>: Нажмите команду `/models` в боте и переключитесь на другую модель (например, <code>claude-sonnet</code> или <code>gemini-flash-high</code>)."
        )
    return None


def markdown_to_html(text: str) -> str:
    """Convert standard markdown formatting to Telegram-compatible HTML tags with safe character escaping."""
    if not text:
        return ""
    # Safe HTML escaping to prevent Telegram entity parsing errors
    escaped = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    lines = escaped.split("\n")
    processed_lines = []
    for line in lines:
        if line.strip() in ("---", "***", "___"):
            processed_lines.append("───────────────")
            continue
        l = line
        # Convert **bold** -> <b>bold</b>
        l = re.sub(r"\*\*(.*?)\*\*", r"<b>\1</b>", l)
        # Convert `code` -> <code>code</code>
        l = re.sub(r"`(.*?)`", r"<code>\1</code>", l)
        processed_lines.append(l)
    return "\n".join(processed_lines)


def format_dyslexia_friendly_text(raw_screen_display: list[str]) -> str:
    """Transform raw PTY terminal screen lines into clean, dyslexia-friendly formatted text.

    1. Filters TUI banners, prompt echoes, status lines.
    2. Intercepts eligibility & auth errors with friendly instructions.
    3. Unwraps mid-sentence terminal line wraps into natural flowing paragraphs.
    4. Applies generous spacing (double newlines) and clean bullet points.
    5. Converts to Telegram-safe HTML formatting.
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

    joined_text = "\n\n".join(paragraphs).strip()
    return markdown_to_html(joined_text)
