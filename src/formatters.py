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
    if any(c in s for c in ["▄", "▀", "█", "▌", "▐", "┌", "┐", "└", "┘", "├", "┤", "────", "│", "─", "╭", "╮", "╰", "╯"]):
        return True

    lower = s.lower()
    # Filter CLI header banners, status bars, and reasoning traces
    if any(pattern in lower for pattern in [
        "antigravity cli", "claude-", "gpt-", "gemini",
        "generating...", "ctrl+c", "reasoning effort", "execution mode",
        "thought for", "prioritizing tool", "tool usage", "? for shortcuts"
    ]):
        # Check if the line ONLY contains the noise, or if it's mixed with actual content
        # If it's too long, it might contain actual content appended to it!
        if len(s) < 80:
            return True

    # Filter prompt echoes and shell path prompts
    if s.startswith("›") or s.startswith("❯") or s.startswith("»") or "~/" in s or "labdoctorm" in lower:
        return True

    if prompt:
        clean_prompt = prompt.strip()
        # Only filter exact matches or very obvious echoes, not just 'contains'
        if s == clean_prompt or s.startswith(f"> {clean_prompt}") or s.startswith(f"› {clean_prompt}") or s.startswith(f"❯ {clean_prompt}") or s.startswith(f"» {clean_prompt}"):
            return True

    return False


def check_known_errors(text: str) -> str | None:
    """Intercept known CLI errors and return clean, dyslexia-friendly HTML alerts."""
    lower = text.lower()
    if "eligibility check failed" in lower or "not eligible for antigravity" in lower:
        return (
            "⚠️ <b>Account Access Error (Eligibility Check)</b>\n\n"
            "The current Google account or your server's region is not supported by the Antigravity service.\n\n"
            "───────────────\n\n"
            "📌 <b>What happened:</b>\n"
            "Google restricts access to Antigravity for certain geolocations and account types.\n\n"
            "💡 <b>How to solve this problem (3 simple steps):</b>\n\n"
            "1. <b>Log into a different account on the server</b>:\n"
            "   Run the following command in the server terminal:\n"
            "   <code>agy auth login</code>\n\n"
            "2. <b>Check proxy or VPN</b>:\n"
            "   Make sure network traffic goes through a supported region.\n\n"
            "3. <b>Automatic update</b>:\n"
            "   After login, the bot will automatically pick up the new account thanks to Hot Reload!"
        )
    if "quota exceeded" in lower or "resource has been exhausted" in lower:
        return (
            "⚠️ <b>Request Limit Reached (Quota Exceeded)</b>\n\n"
            "The current model has exhausted its daily request limit.\n\n"
            "💡 <b>Solution</b>: Tap the `/models` command in the bot and switch to a different model (for example, <code>claude-sonnet</code> or <code>gemini-flash-high</code>)."
        )
    if "select login method" in lower or "accounts.google.com/o/oauth2" in lower:
        return (
            "⚠️ <b>Google Authorization Required</b>\n\n"
            "Google login is not completed on your server for the Antigravity CLI account.\n\n"
            "───────────────\n\n"
            "💡 <b>How to log in (2 simple steps):</b>\n\n"
            "1. <b>Run in the server terminal:</b>\n"
            "   <code>agy auth login</code>\n\n"
            "2. <b>Follow the link from the console</b> and authorize in Google.\n\n"
            "After login, the bot will automatically pick up your account!"
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


import uuid

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
    placeholder_prefix = f"__CODE_BLOCK_{uuid.uuid4().hex[:8]}_"

    def save_code_block(match):
        lang = match.group(1) or ""
        code_content = match.group(2)
        escaped_code = code_content.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        idx = len(code_blocks)
        class_attr = f' class="language-{lang.strip()}"' if lang.strip() else ""
        code_blocks.append(f'<pre><code{class_attr}>{escaped_code}</code></pre>')
        return f"{placeholder_prefix}{idx}__"

    text_processed = re.sub(r"```(\w*)\n?(.*?)```", save_code_block, text, flags=re.DOTALL)
    escaped = text_processed.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    for idx, cb in enumerate(code_blocks):
        escaped = escaped.replace(f"{placeholder_prefix}{idx}__", cb)

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
            line_clean = re.sub(r"^[>›»❯\?]\s*", "", line)
            if prompt_snippet:
                if (len(prompt_snippet) >= 3 and prompt_snippet in line_clean) or line_clean.strip() == clean_prompt.strip():
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
        
        # Scrub Pyte wrap garble artifacts
        l = re.sub(r"esc to cancel.*?(low|high|pro|sonnet|opus|haiku)", "", l, flags=re.IGNORECASE)
        l = re.sub(r"\? for shortcuts.*?(low|high|pro|sonnet|opus|haiku)", "", l, flags=re.IGNORECASE)
        l = re.sub(r"Gemini 3\..*?(low|high|pro)", "", l, flags=re.IGNORECASE)
        l = re.sub(r"● Bash.*?(expand\))", "", l, flags=re.IGNORECASE)
        
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
    """Format agy /usage raw screen lines into a clean, 100% English HTML Telegram report."""
    raw_joined = "\n".join(lines) if isinstance(lines, list) else str(lines)
    lines_list = raw_joined.split("\n")

    groups = []
    current_group = None
    current_limit_type = None

    for line in lines_list:
        l_strip = line.strip()
        # Clean TUI border noise
        for c in ["│", "─", "┌", "┐", "└", "┘", "├", "┤", "┼", "▐", "▌", "█", "▄", "▀"]:
            l_strip = l_strip.replace(c, "")
        l_strip = l_strip.strip()
        l_lower = l_strip.lower()

        if not l_strip or "scroll" in l_lower or "esc close" in l_lower or l_lower.startswith("account:") or "welcome to" in l_lower:
            continue

        # Group Headers (e.g. GEMINI MODELS, CLAUDE AND GPT MODELS)
        if re.match(r"^[A-Z\s&]+MODELS$", l_strip):
            group_name = l_strip.title()
            # Special case for "Claude And Gpt Models" to "Claude/GPT Models" if requested, or just title case
            group_name = group_name.replace("And", "&").replace("Gpt", "GPT")
            
            current_group = {
                "name": group_name,
                "models": "",
                "limits": []
            }
            groups.append(current_group)
            current_limit_type = None
            continue

        if not current_group:
            continue

        if l_strip.startswith("Models within this group:"):
            current_group["models"] = l_strip.replace("Models within this group:", "").strip()
            continue

        if l_strip in ["Weekly Limit Remaining", "Five Hour Limit Remaining"]:
            type_str = l_strip.replace("Remaining", "").strip()
            
            # Setup for next lines
            current_limit_type = type_str
            current_group["limits"].append({
                "type": type_str,
                "val": "",
                "refreshes": ""
            })
            continue

        if current_limit_type:
            # Look for percentage or disabled text
            pct_match = re.search(r"(\d+(?:\.\d+)?)\s*%", l_strip)
            ref_match = re.search(r"Refreshes in\s+([0-9h\s+m]+)", l_strip, re.IGNORECASE)
            
            target_limit = next((l for l in current_group["limits"] if l["type"] == current_limit_type), None)
            if target_limit:
                if pct_match and not target_limit["val"]:
                    target_limit["val"] = f"{pct_match.group(1)}% remaining"
                elif l_strip.startswith("Disabled:"):
                    target_limit["val"] = "Disabled (Weekly limit reached)"
                
                if ref_match and not target_limit["refreshes"]:
                    target_limit["refreshes"] = ref_match.group(1).strip()

    output_parts = [
        "📊 <b>Model Quotas & Limits Report</b>\n",
        f"👤 <b>Account:</b> <code>{email}</code>\n" if email else ""
    ]

    # Filter out empty groups
    valid_groups = [g for g in groups if g["limits"]]

    if not valid_groups:
        # Fallback formatting if raw screen didn't capture structured modal
        output_parts.append("<i>Could not parse modal overlay automatically. Raw view:</i>\n")
        clean_raw = "\n".join([l for l in lines_list if l.strip() and "welcome to" not in l.lower() and "▄" not in l])
        output_parts.append(f"<pre><code>{clean_raw[:2500]}</code></pre>")
        return "\n".join(output_parts).strip()

    for g in valid_groups:
        g_name = g["name"]
        g_models = g["models"]
        output_parts.append(f"🔹 <b>{g_name}</b>")
        if g_models:
            output_parts.append(f"   • <i>Models:</i> <code>{g_models}</code>")

        for lim in g["limits"]:
            l_type = lim["type"]
            val = lim["val"]
            ref = lim["refreshes"]
            ref_text = f" (Refreshes in <code>{ref}</code>)" if ref else ""
            output_parts.append(f"   • {l_type}: <b>{val}</b>{ref_text}")

        output_parts.append("")

    return "\n".join(output_parts).strip()
