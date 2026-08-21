import os
import re
import json
import urllib.request
import urllib.error

def main():
    raw_vault = os.environ.get("CF_TOKENS", "")
    
    # 1. Extract the token and account ID
    # The token looks like cfut_...
    token_match = re.search(r"cfut_[a-zA-Z0-9]+", raw_vault)
    if not token_match:
        print("❌ Error: Could not find cfut_ token in vault.")
        return
    cf_token = token_match.group(0)
    
    # The account ID is a 32-character hex string
    # E.g., 34ba88610ade55bc07b1244274acfebc
    account_match = re.search(r"\b[a-f0-9]{32}\b", raw_vault)
    if not account_match:
        print("❌ Error: Could not find 32-char Account ID in vault.")
        return
    account_id = account_match.group(0)
    
    print(f"✅ Found CF Token and Account ID: {account_id[:8]}...")
    
    # 2. Prepare the dummy diff
    dummy_diff = """
--- a/calculator.py
+++ b/calculator.py
@@ -10,3 +10,6 @@
 def add(a, b):
     return a + b
+
+def divide(a, b):
+    # Fast division
+    return a / b
    """
    
    # 3. Request to Cloudflare Workers AI
    model = "@cf/meta/llama-3.1-8b-instruct"
    url = f"https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/run/{model}"
    
    headers = {
        "Authorization": f"Bearer {cf_token}",
        "Content-Type": "application/json"
    }
    
    payload = {
        "messages": [
            {
                "role": "system",
                "content": "You are a ruthless, expert code auditor. Analyze the given git diff. Return ONLY a JSON array of findings with keys: 'severity', 'description'. If the code is perfect, return an empty array []."
            },
            {
                "role": "user",
                "content": f"Audit this diff:\n{dummy_diff}"
            }
        ]
    }
    
    print(f"🚀 Sending PoC Audit request to Cloudflare ({model})...")
    
    data = json.dumps(payload).encode('utf-8')
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            res_data = json.loads(response.read().decode('utf-8'))
            print("✅ Response received!")
            
            # The AI's response is usually in res_data['result']['response']
            if 'result' in res_data and 'response' in res_data['result']:
                print("\n--- 🤖 Llama-3 Audit Result ---")
                print(res_data['result']['response'])
                print("--------------------------------")
            else:
                print("Unexpected response format:")
                print(json.dumps(res_data, indent=2))
                
    except urllib.error.HTTPError as e:
        error_msg = e.read().decode('utf-8')
        print(f"❌ API HTTP Error {e.code}: {error_msg}")
    except Exception as e:
        print(f"❌ Error: {e}")

if __name__ == "__main__":
    main()
