import os
import sys
import re
import json
import random
import urllib.request
import urllib.error

def main():
    if len(sys.argv) < 2:
        print("❌ Error: Missing prompt argument.")
        print("Usage: python3 dispatcher.py <prompt>")
        sys.exit(1)
        
    prompt = sys.argv[1]
    raw_keys = os.environ.get("MANUS_KEYS", "")
    
    # 1. Parse keys
    keys = re.findall(r"sk-[a-zA-Z0-9_-]+", raw_keys)
    if not keys:
        print("❌ Error: No valid Manus API keys found in the MANUS_KEYS environment variable.")
        sys.exit(1)
        
    # 2. Load Balancing (Random Pick)
    selected_key = random.choice(keys)
    print(f"🤖 Manus Swarm Dispatcher: Selected 1 of {len(keys)} available keys for load balancing.")
    
    # 3. Prepare Request
    url = "https://api.manus.ai/v2/task.create"
    headers = {
        "x-manus-api-key": selected_key,
        "Content-Type": "application/json"
    }
    
    payload = {
        "title": "Delegated Task from Antigravity",
        "message": {"content": prompt},
        "agent_profile": "manus-1.6-lite"
    }
    
    data = json.dumps(payload).encode('utf-8')
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    
    # 4. Dispatch Task (With 10s timeout to prevent Bot crash)
    print(f"🚀 Dispatching task to Manus Cloud (10s timeout)...")
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            res_data = json.loads(response.read().decode('utf-8'))
            print("✅ Task successfully created!")
            print(json.dumps(res_data, indent=2))
    except urllib.error.HTTPError as e:
        error_msg = e.read().decode('utf-8')
        print(f"❌ API HTTP Error {e.code}: {error_msg}")
    except Exception as e:
        print(f"❌ Dispatcher Error: {e}")

if __name__ == "__main__":
    main()
