import os
import re
from pathlib import Path

def main():
    base_dir = Path("/root/LabDoctorM/projects/antigravity-telegram-agent")
    
    # We will walk through the directory and update contents
    for root, dirs, files in os.walk(base_dir):
        if '.git' in root or '.venv' in root or '__pycache__' in root or 'test_dir' in root:
            continue
        
        for file in files:
            filepath = Path(root) / file
            if filepath.suffix in ['.py', '.md', '.sh', '.toml', '.service', '.json', '.txt', '.env', '.example', '']:
                try:
                    content = filepath.read_text(encoding='utf-8')
                    
                    # Store original to check if modified
                    original_content = content
                    
                    # 1. Update paths and systemd service names
                    content = content.replace("projects/antigravity-telegram-agent", "projects/antigravity-telegram-agent")
                    content = content.replace("antigravity-telegram-agent.service", "antigravity-telegram-agent.service")
                    content = content.replace("systemctl status antigravity-telegram-agent", "systemctl status antigravity-telegram-agent")
                    content = content.replace("systemctl restart antigravity-telegram-agent", "systemctl restart antigravity-telegram-agent")
                    content = content.replace("systemctl stop antigravity-telegram-agent", "systemctl stop antigravity-telegram-agent")
                    content = content.replace("systemctl enable --now antigravity-telegram-agent", "systemctl enable --now antigravity-telegram-agent")
                    content = content.replace("journalctl -u antigravity-telegram-agent -f", "journalctl -u antigravity-telegram-agent -f")
                    
                    # 2. Update DB and artifact names
                    content = content.replace("antigravity-telegram-agent.db", "antigravity-telegram-agent.db")
                    content = content.replace("agent_response.md", "agent_response.md")
                    
                    # 3. Update Git links
                    content = content.replace("thedoctormes-hue/antigravity-telegram-agent", "thedoctormes-hue/antigravity-telegram-agent")
                    content = content.replace("antigravity-telegram-agent.git", "antigravity-telegram-agent.git")
                    
                    # 4. Update UI texts and general occurrences
                    # We will replace AntigravityTelegramAgent with AntigravityTelegramAgent or Antigravity Telegram Agent
                    content = content.replace("Antigravity Telegram Agent Control Center", "Antigravity Telegram Agent Control Center")
                    content = content.replace("AntigravityTelegramAgent", "AntigravityTelegramAgent")
                    
                    # 5. Fix any lowercase left
                    content = content.replace("antigravity-telegram-agent", "antigravity-telegram-agent")
                    
                    if content != original_content:
                        filepath.write_text(content, encoding='utf-8')
                        print(f"Updated: {filepath}")
                except Exception as e:
                    print(f"Failed to read/write {filepath}: {e}")

if __name__ == '__main__':
    main()
