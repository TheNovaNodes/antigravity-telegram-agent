import re

def clean_tui_response(raw_text: str, prompt: str) -> str:
    text = raw_text.replace('\r', '')
    text = re.sub(r'(?:\x1B[@-_]|[\x80-\x9F])[0-?]*[ -/]*[@-~]', '', text)
    text = re.sub(r'[⣾⣽⣻⢿⡿⣟⣯⣷⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]', '', text)
    text = re.sub(r'─{3,}', '', text)
    
    lines = text.split('\n')
    clean_lines = []
    
    for line in lines:
        l = line.strip()
        if not l:
            continue
        if l.startswith('>') or l == 'esc to cancel':
            continue
        if 'Generating...' in l or 'esc to cancel' in l:
            continue
        if re.search(r'(Gemini|Claude|GPT-OSS)', l, re.IGNORECASE):
            continue
        if l.lower() == prompt.lower() or l.lower().startswith(prompt.lower()):
            continue
        clean_lines.append(l)
        
    final_lines = []
    for l in clean_lines:
        if not final_lines or final_lines[-1] != l:
            final_lines.append(l)
            
    return '\n'.join(final_lines)

raw_sample = """При\n\n Gвет\r\n\n────────────────────────────────────────────────────────────────────────────────\r\n Gemini 3.6 Flash · low\r\r\n> Привет\r\n⣾  Generating...\r\n\n>\r\n\n────────────────────────────────────────────────────────────────────────────────\r\nesc to cancel                                             Gemini 3.6 Flash · low\renerat\n\n\n\r\n  Привет\r\n⣾  Generating...\r\n────────────────────────────────────────────────────────────────────────────────\r\n>\r\n\n────────────────────────────────────────────────────────────────────────────────\r\nesc to cancel                                             Gemini 3.6 Flash · low\r\t! Чем я могу вам помочь сегодня? Расскажите о вашей задаче или\r\n  проекте!\r\n\n────────────────────────────────────────────────────────────────────────────────\r\n Gemini 3.6 Flash · low\r"""

print("--- RESULT ---")
print(repr(clean_tui_response(raw_sample, "Привет")))
print("--------------")
print(clean_tui_response(raw_sample, "Привет"))
