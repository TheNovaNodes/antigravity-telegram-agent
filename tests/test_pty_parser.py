import pyte
import re

def sanitize_chunk_for_pyte(chunk: bytes) -> bytes:
    """Deterministic parser/filter for CSI sequences before pyte."""
    return re.sub(
        br'\x1b\[[=>?]*[0-9;]*[a-zA-Z]', 
        lambda m: b'' if m.group(0) in [b'\x1b[=1;1u', b'\x1b[>4;2m'] else m.group(0), 
        chunk
    )

def parse_pty_stream(stream_bytes: bytes, cols: int = 500, lines: int = 3000) -> str:
    """Simulates the CLI runner PTY extraction."""
    screen = pyte.Screen(cols, lines)
    stream = pyte.ByteStream(screen)
    
    sanitized = sanitize_chunk_for_pyte(stream_bytes)
    stream.feed(sanitized)
    
    display = []
    for l in screen.display:
        s = l.rstrip()
        if s:
            display.append(s)
    return "\n".join(display)

def test_pty_ansi_csi():
    raw = b"Hello\x1b[31m World\x1b[0m\r\nLine 2"
    assert parse_pty_stream(raw) == "Hello World\nLine 2"

def test_pty_osc():
    raw = b"Before\x1b]0;Title\x07After\x1b]133;A\x07"
    assert parse_pty_stream(raw) == "BeforeAfter"

def test_pty_broken_escape():
    raw = b"Test\x1b[31"
    assert isinstance(parse_pty_stream(raw), str)

def test_pty_carriage_return():
    raw = b"Hello\rWorld"
    assert parse_pty_stream(raw) == "World"

def test_pty_line_wrapping():
    raw = b"A" * 500 + b"B"
    assert parse_pty_stream(raw, cols=500, lines=30) == "A" * 500 + "\nB"

def test_pty_unicode():
    raw = "Привет 🌍".encode("utf-8")
    assert parse_pty_stream(raw) == "Привет 🌍"

def test_pty_markdown_tables():
    raw = (
        b"| Header 1 | Header 2 |\r\n"
        b"| -------- | -------- |\r\n"
        b"| Row 1    | Data 1   |"
    )
    result = parse_pty_stream(raw)
    assert "| Header 1 | Header 2 |" in result
    assert "| Row 1    | Data 1   |" in result
