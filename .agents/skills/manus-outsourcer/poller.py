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
        
    # We don't know which key was used to create the task, but typically we can just try the first one,
    # or iterate through them until we get a 200 OK.
    url = f"https://api.manus.ai/v2/tasks/{task_id}" # Assuming standard REST endpoint
    
    success = False
    for key in keys:
        headers = {"x-manus-api-key": key}
        req = urllib.request.Request(url, headers=headers, method="GET")
        
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                res_data = json.loads(response.read().decode('utf-8'))
                print("✅ Task Status Retrieved:")
                status = res_data.get("status", "unknown")
                print(f"Status: {status}")
                if status in ["completed", "done", "error", "stopped"]:
                    print(json.dumps(res_data, indent=2))
                success = True
                break
        except urllib.error.HTTPError as e:
            if e.code in [401, 403, 404]:
                continue # Try next key
            else:
                print(f"❌ API Error {e.code}: {e.read().decode('utf-8')}")
                break
        except Exception as e:
            continue
            
    if not success:
        print(f"❌ Failed to retrieve status for task {task_id}. Make sure the endpoint is correct.")

if __name__ == "__main__":
    main()
