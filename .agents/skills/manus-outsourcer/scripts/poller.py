import urllib.request
import json
import sys
import os

def main():
    if len(sys.argv) < 2:
        print("Usage: python3 poller.py <task_id>")
        sys.exit(1)
        
    task_id = sys.argv[1]
    keys_env = os.environ.get("MANUS_KEYS", "")
    import re
    keys = re.findall(r"sk-[a-zA-Z0-9_-]+", keys_env)
    
    if not keys:
        print("❌ Error: No valid Manus API keys found in MANUS_KEYS.")
        sys.exit(1)
        
    url = f"https://api.manus.ai/v2/task.listMessages?task_id={task_id}&order=asc&limit=100"
    
    success = False
    for key in keys:
        headers = {"x-manus-api-key": key}
        req = urllib.request.Request(url, headers=headers, method="GET")
        
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                res_data = json.loads(response.read().decode('utf-8'))
                
                messages = res_data.get("messages", [])
                if messages:
                    print(f"✅ Found task {task_id} on this key!")
                    success = True
                    
                    final_status = "running"
                    attachments = []
                    
                    for msg in messages:
                        if msg.get("type") == "status_update":
                            status_update = msg.get("status_update", {})
                            if status_update.get("agent_status") == "stopped":
                                final_status = "completed (stopped)"
                        elif msg.get("type") == "assistant_message":
                            ast_msg = msg.get("assistant_message", {})
                            if ast_msg.get("attachments"):
                                for att in ast_msg["attachments"]:
                                    if att.get("url"):
                                        attachments.append(att["url"])
                    
                    print(f"Status: {final_status}")
                    
                    if attachments:
                        print("\n📎 Attachments found:")
                        for idx, att_url in enumerate(attachments):
                            print(f"[{idx+1}] {att_url}")
                            
                    break
        except Exception as e:
            continue
            
    if not success:
        print(f"❌ Failed to find messages for task {task_id} on any of the keys.")

if __name__ == "__main__":
    main()
