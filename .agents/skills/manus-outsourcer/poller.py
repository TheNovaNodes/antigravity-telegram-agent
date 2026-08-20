import os
import sys
import re
import json
import urllib.request
import urllib.error

def main():
    if len(sys.argv) < 2:
        print("❌ Error: Missing task_id argument.")
        print("Usage: python3 poller.py <task_id>")
        sys.exit(1)
        
    task_id = sys.argv[1]
    raw_keys = os.environ.get("MANUS_KEYS", "")
    
    keys = re.findall(r"sk-[a-zA-Z0-9_-]+", raw_keys)
    if not keys:
        print("❌ Error: No valid Manus API keys found.")
        sys.exit(1)
        
    url = "https://api.manus.ai/v2/task.list"
    
    success = False
    for key in keys:
        headers = {"x-manus-api-key": key}
        req = urllib.request.Request(url, headers=headers, method="GET")
        
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                res_data = json.loads(response.read().decode('utf-8'))
                
                # Search for the task in the data array
                task_data = None
                for t in res_data.get("data", []):
                    if t.get("id") == task_id:
                        task_data = t
                        break
                        
                if task_data:
                    print(f"✅ Found task {task_id} on this key!")
                    status = task_data.get("status", "unknown")
                    print(f"Status: {status}")
                    print(json.dumps(task_data, indent=2))
                    success = True
                    break
        except Exception as e:
            continue
            
    if not success:
        print(f"❌ Failed to find task {task_id} on any of the 5 keys.")

if __name__ == "__main__":
    main()
