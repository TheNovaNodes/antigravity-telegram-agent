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


def highlight_tech_terms(text: str) -> str:
    """Highlight standalone technical terms, filenames, and paths in mono font for dyslexia readability."""
    # Pattern matching file paths and files with extensions like src/db.py, config.json, main.py
    path_pattern = r'(?<![A-Za-z0-9_/<>&;`"])(\b[a-zA-Z0-9_\-\.]+\/[a-zA-Z0-9_\-\./\.]+\b|\b[a-zA-Z0-9_\-]+\.(?:py|json|md|txt|sh|html|css|js|yml|yaml|toml|service)\b)(?![A-Za-z0-9_/<>&;`"])'
    text = re.sub(path_pattern, r'<code>\1</code>', text)
    return text


def markdown_to_html(text: str) -> str:
    """Convert Markdown syntax to full Telegram-compatible Rich Text HTML safely.

    Handles:
    - Code blocks ```lang ... ``` -> <pre><code class="language-lang">...</code></pre>
    - Inline code `code` -> <code>code</code>
    - Bold **text** / __text__ -> <b>text</b>
    - Italic *text* -> <i>text</i>
    - Headers # Header -> <b><u>HEADER</u></b>
    - Blockquotes > quote -> <blockquote>quote</blockquote>
    - Links [text](url) -> <a href="url">text</a>
    - Separators --- -> horizontal line
    """
    if not text:
        return ""

    # Preserve multi-line code blocks during escaping
    code_blocks = []

    def save_code_block(match):
        lang = match.group(1) or ""
        code_content = match.group(2)
        # Escape code block content safely
        escaped_code = code_content.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        idx = len(code_blocks)
        class_attr = f' class="language-{lang.strip()}"' if lang.strip() else ""
        code_blocks.append(f'<pre><code{class_attr}>{escaped_code}</code></pre>')
        return f"__CODE_BLOCK_{idx}__"

    # Extract ```lang\ncode``` blocks first
    text_processed = re.sub(r"```(\w*)\n?(.*?)```", save_code_block, text, flags=re.DOTALL)

    # Safe HTML escaping for main text
    escaped = text_processed.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    # Restore code blocks back (their contents are already safely escaped)
    for idx, cb in enumerate(code_blocks):
        escaped = escaped.replace(f"__CODE_BLOCK_{idx}__", cb)

    lines = escaped.split("\n")
    processed_lines = []
    in_quote = False
    quote_buffer = []

    def flush_quote():
        nonlocal quote_buffer, in_quote
        if quote_buffer:
            q_text = "\n".join(quote_buffer)
            processed_lines.append(f"<blockquote>{q_text}</blockquote>")
            quote_buffer = []
        in_quote = False

    for line in lines:
        l = line.rstrip()
        stripped = l.strip()

        # Check separator lines
        if stripped in ("---", "***", "___", "───────────────"):
            flush_quote()
            processed_lines.append("───────────────")
            continue

        # Check blockquotes (> quote)
        if stripped.startswith("&gt; ") or stripped.startswith("> "):
            in_quote = True
            q_line = stripped[5:] if stripped.startswith("&gt; ") else stripped[2:]
            quote_buffer.append(q_line)
            continue
        elif in_quote:
            flush_quote()

        # Check Headers (# Header, ## Header, ### Header)
        header_match = re.match(r"^(#{1,6})\s+(.+)$", stripped)
        if header_match:
            header_text = header_match.group(2)
            # Format header clearly for reading
            l = f"<b><u>{header_text}</u></b>"
            processed_lines.append("")
            processed_lines.append(l)
            continue

        # Convert Markdown formatting outside code blocks
        if "__CODE_BLOCK_" not in l and "<pre>" not in l:
            # Convert **bold** -> <b>bold</b>
            l = re.sub(r"\*\*(.*?)\*\*", r"<b>\1</b>", l)
            l = re.sub(r"__(.*?)__", r"<b>\1</b>", l)
            # Convert `code` -> <code>code</code>
            l = re.sub(r"`(.*?)`", r"<code>\1</code>", l)
            # Convert [text](url) -> <a href="url">text</a>
            l = re.sub(r"\[(.*?)\]\((https?://\S+)\)", r'<a href="\2">\1</a>', l)
            # Convert *italic* -> <i>italic</i>
            l = re.sub(r"(?<!\w)\*(.*?)\*(?!\w)", r"<i>\1</i>", l)

            # Auto-highlight standalone tech terms and file paths if not already in HTML tags
            l = highlight_tech_terms(l)

        processed_lines.append(l)

    flush_quote()
    return "\n".join(processed_lines)


def format_dyslexia_friendly_text(raw_screen_display: list[str]) -> str:
    """Transform raw PTY terminal screen lines into clean, dyslexia-friendly formatted text.

    1. Filters TUI banners, prompt echoes, status lines.
    2. Intercepts eligibility & auth errors with friendly instructions.
    3. Unwraps mid-sentence terminal line wraps into natural flowing paragraphs.
    4. Applies generous spacing (double newlines) and clean bullet points for low cognitive load.
    5. Converts to Telegram-safe Rich Text HTML formatting.
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
            # Paragraph boundary triggers: sentence end punctuation, list items, headers, code blocks
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


def format_usage_response(lines, email: str = "") -> str:
    """Format agy /usage raw screen lines into a comprehensive, dyslexia-friendly HTML Telegram report."""
    raw_joined = "\n".join(lines) if isinstance(lines, list) else str(lines)
    lines_list = raw_joined.split("\n")
    
    skip_keywords = [
        "scroll", "pgup", "pgdown", "page", "ctrl+", "esc close", 
        "models & quota", "all models", "welcome to", "signed in",
        "view your available", "quota refreshes"
    ]
    
    parsed_models = []
    current_model = None

    for line in lines_list:
        l_strip = line.strip()
        l_lower = l_strip.lower()
        
        if not l_strip or any(k in l_lower for k in skip_keywords) or l_lower.startswith("account:"):
            continue

        if any(brand in l_strip for brand in ["Gemini", "Claude", "GPT"]):
            if "separate quota pools" in l_lower:
                continue
            current_model = {
                "name": l_strip,
                "pct": 100,
                "refreshes": ""
            }
            parsed_models.append(current_model)
        elif current_model:
            if "% remaining" in l_strip:
                pct_match = re.search(r"(\d+)%\s+remaining", l_strip)
                if pct_match:
                    current_model["pct"] = int(pct_match.group(1))
                if "Refreshes in" in l_strip:
                    current_model["refreshes"] = l_strip.split("Refreshes in")[1].strip()

    seen = set()
    unique_models = []
    for m in parsed_models:
        if m["name"] not in seen:
            seen.add(m["name"])
            unique_models.append(m)

    output_parts = [
        "📊 <b>Подробный отчет по квотам и лимитам моделей</b>\n",
        f"👤 <b>Аккаунт:</b> <code>{email}</code>\n" if email else ""
    ]

    for m in unique_models:
        name = m["name"]
        pct = m["pct"]
        ref = m["refreshes"]
        green_blocks = int(pct / 10)
        black_blocks = 10 - green_blocks
        bar = "🟩" * green_blocks + "⬛" * black_blocks
        
        ref_text = f"\n   • <i>Сброс лимита через:</i> <code>{ref}</code>" if ref else ""
        output_parts.append(
            f"🔹 <b>{name}</b>\n"
            f"   Остаток квоты: <b>{pct}%</b> ({bar}){ref_text}\n"
        )

    return "\n".join(output_parts)

