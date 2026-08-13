import re
import logging

logger = logging.getLogger(__name__)


def is_tui_noise(line: str, prompt: str = "") -> bool:
    """Detect if a line is terminal ASCII noise, TUI status bar, banner, or prompt echo."""
    s = line.strip()
    if not s:
        return True

    # Allow Markdown table lines starting with pipe
    if s.startswith("|"):
        return False

    # Filter ASCII art characters and TUI box borders
    if any(c in s for c in ["▄", "▀", "█", "▌", "▐", "┌", "┐", "└", "┘", "├", "┤", "────"]):
        return True

    lower = s.lower()
    # Filter CLI header banners, status bars, and reasoning traces
    if any(pattern in lower for pattern in [
        "antigravity cli", "gemini 3.", "claude-", "gpt-", "esc to cancel",
        "generating...", "ctrl+c", "reasoning effort", "execution mode",
        "thought for", "prioritizing tool", "tool usage", "? for shortcuts"
    ]):
        return True

    # Filter prompt echoes and shell path prompts
    if s.startswith(">") or s.startswith("›") or s.startswith("❯") or s.startswith("»") or "~/" in s or "labdoctorm" in lower or "projects" in lower:
        return True

    if prompt:
        clean_p = prompt.replace("\n", " ").strip()
        if clean_p and (s == clean_p or s == f"> {clean_p}" or s == f"› {clean_p}"):
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
    if "select login method" in lower or "accounts.google.com/o/oauth2" in lower:
        return (
            "⚠️ <b>Требуется авторизация Google</b>\n\n"
            "На вашем сервере не завершен вход в аккаунт Antigravity CLI.\n\n"
            "───────────────\n\n"
            "💡 <b>Как войти (2 простых шага):</b>\n\n"
            "1. <b>Выполните в терминале сервера:</b>\n"
            "   <code>agy auth login</code>\n\n"
            "2. <b>Перейдите по ссылке из консоли</b> и авторизуйтесь в Google.\n\n"
            "После входа бот автоматически подхватит ваш аккаунт!"
        )
    return None


def highlight_tech_terms(text: str) -> str:
    """Highlight standalone technical terms, filenames, and paths in mono font for dyslexia readability."""
    path_pattern = r'(?<![A-Za-z0-9_/<>&;`"])(\b[a-zA-Z0-9_\-\.]+\/[a-zA-Z0-9_\-\./\.]+\b|\b[a-zA-Z0-9_\-]+\.(?:py|json|md|txt|sh|html|css|js|yml|yaml|toml|service)\b)(?![A-Za-z0-9_/<>&;`"])'
    text = re.sub(path_pattern, r'<code>\1</code>', text)
    return text


def format_table_block(table_lines: list[str]) -> str:
    """Format markdown table lines into clean monospace Rich Text HTML."""
    if not table_lines:
        return ""
    joined = "\n".join(table_lines)
    escaped = joined.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return f"<pre><code>{escaped}</code></pre>"


def markdown_to_html(text: str) -> str:
    """Convert Markdown syntax to full Telegram-compatible Rich Text HTML safely.

    Handles:
    - Code blocks ```lang ... ``` -> <pre><code class="language-lang">...</code></pre>
    - Tables | col | col | -> <pre><code>...</code></pre>
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
        escaped_code = code_content.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        idx = len(code_blocks)
        class_attr = f' class="language-{lang.strip()}"' if lang.strip() else ""
        code_blocks.append(f'<pre><code{class_attr}>{escaped_code}</code></pre>')
        return f"__CODE_BLOCK_{idx}__"

    text_processed = re.sub(r"```(\w*)\n?(.*?)```", save_code_block, text, flags=re.DOTALL)
    escaped = text_processed.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    for idx, cb in enumerate(code_blocks):
        escaped = escaped.replace(f"__CODE_BLOCK_{idx}__", cb)

    lines = escaped.split("\n")
    processed_lines = []
    in_quote = False
    quote_buffer = []
    table_buffer = []

    def flush_quote():
        nonlocal quote_buffer, in_quote
        if quote_buffer:
            q_text = "\n".join(quote_buffer)
            processed_lines.append(f"<blockquote>{q_text}</blockquote>")
            quote_buffer = []
        in_quote = False

    def flush_table():
        nonlocal table_buffer
        if table_buffer:
            t_text = "\n".join(table_buffer)
            processed_lines.append(f"<pre><code>{t_text}</code></pre>")
            table_buffer = []

    for line in lines:
        l = line.rstrip()
        stripped = l.strip()

        # Handle Markdown Tables (lines starting with '|')
        if stripped.startswith("|") and stripped.endswith("|"):
            flush_quote()
            table_buffer.append(stripped)
            continue
        elif table_buffer:
            flush_table()

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
            l = f"<b><u>{header_text}</u></b>"
            processed_lines.append("")
            processed_lines.append(l)
            continue

        # Convert Markdown formatting outside code blocks
        if "__CODE_BLOCK_" not in l and "<pre>" not in l:
            l = re.sub(r"\*\*(.*?)\*\*", r"<b>\1</b>", l)
            l = re.sub(r"__(.*?)__", r"<b>\1</b>", l)
            l = re.sub(r"`(.*?)`", r"<code>\1</code>", l)
            l = re.sub(r"\[(.*?)\]\((https?://\S+)\)", r'<a href="\2">\1</a>', l)
            l = re.sub(r"(?<!\w)\*(.*?)\*(?!\w)", r"<i>\1</i>", l)
            l = highlight_tech_terms(l)

        processed_lines.append(l)

    flush_quote()
    flush_table()
    return "\n".join(processed_lines)


def extract_new_response_lines(raw_screen_display: list[str], prompt: str = "") -> list[str]:
    """Isolate and extract ONLY the new response generated after the user's prompt."""
    if not raw_screen_display:
        return []

    clean_prompt = prompt.replace("\n", " ").strip() if prompt else ""
    prompt_idx = -1

    if clean_prompt:
        prompt_snippet = clean_prompt[:30].strip()
        for idx in range(len(raw_screen_display) - 1, -1, -1):
            line = raw_screen_display[idx].strip()
            # Only consider actual prompt lines to avoid matching AI responses that repeat the prompt
            if line.startswith("> ") or line.startswith("› ") or line.startswith("❯ ") or line.startswith("» "):
                line_clean = re.sub(r"^[>›»❯\?]\s*", "", line)
                if prompt_snippet and len(prompt_snippet) >= 5 and prompt_snippet in line_clean:
                    prompt_idx = idx
                    break

    if prompt_idx == -1:
        for idx in range(len(raw_screen_display) - 1, -1, -1):
            line = raw_screen_display[idx].strip()
            if line.startswith(">") or line.startswith("›") or line.startswith("❯") or line.startswith("»"):
                prompt_idx = idx
                break

    if prompt_idx != -1:
        start_idx = prompt_idx + 1
        while start_idx < len(raw_screen_display):
            line = raw_screen_display[start_idx].strip()
            line_clean = re.sub(r"^[>›»❯\?]\s*", "", line)
            if clean_prompt and line_clean and len(line_clean) > 3 and line_clean in clean_prompt:
                start_idx += 1
            else:
                break
        return raw_screen_display[start_idx:]

    return raw_screen_display


def format_dyslexia_friendly_text(raw_screen_display: list[str], prompt: str = "") -> str:
    """Transform raw PTY terminal screen lines into clean, dyslexia-friendly formatted text with Rich Text tables."""
    raw_joined = "\n".join(raw_screen_display)
    known_err = check_known_errors(raw_joined)
    if known_err:
        return known_err

    lines_to_process = extract_new_response_lines(raw_screen_display, prompt)

    cleaned_lines = []
    for line in lines_to_process:
        l = line.rstrip()
        if is_tui_noise(l, prompt):
            continue

        if l.startswith("    "):
            l = l[4:]
        elif l.startswith("   "):
            l = l[3:]
        elif l.startswith("  "):
            l = l[2:]

        cleaned_lines.append(l.strip())

    if not cleaned_lines:
        return ""

    # Paragraph unwrapping and table preserving
    paragraphs = []
    curr = []

    for line in cleaned_lines:
        if not curr:
            curr.append(line)
        else:
            prev = curr[-1]
            # If current or previous is a table row, keep table lines contiguous
            if line.startswith("|") or prev.startswith("|"):
                if line.startswith("|") and prev.startswith("|"):
                    curr.append(line)
                else:
                    paragraphs.append("\n".join(curr) if prev.startswith("|") else " ".join(curr))
                    curr = [line]
                continue

            # Paragraph boundary triggers
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
        paragraphs.append("\n".join(curr) if curr[0].startswith("|") else " ".join(curr))

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
        for c in ["│", "─", "┌", "┐", "└", "┘", "├", "┤", "┼", "▐", "▌", "█", "▄", "▀"]:
            l_strip = l_strip.replace(c, "")
        l_strip = l_strip.strip()
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
            elif "Refreshes in" in l_strip:
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
        
        ref_text = f"\n   • <i>Сброс через:</i> <code>{ref}</code>" if ref else ""
        output_parts.append(
            f"🔹 <b>{name}</b>\n"
            f"   Остаток: <b>{pct}%</b>{ref_text}\n"
        )

    return "\n".join(output_parts)
