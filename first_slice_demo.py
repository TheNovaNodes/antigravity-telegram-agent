import sys
import logging
from src.autonomous_loop.state_machine import BoundedAutonomousLoop

logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")

def dummy_agent(attempt: int, history: list) -> str:
    """Mock agent that fixes the bug on attempt 2."""
    if attempt == 1:
        # Agent tries a wrong fix first
        return """diff --git a/tests/test_artificial.py b/tests/test_artificial.py
index e69de29..d95f3c9 100644
--- a/tests/test_artificial.py
+++ b/tests/test_artificial.py
@@ -1,5 +1,5 @@
 def calculate_value():
-    return 3 # BUG! Should return 4
+    return 5 # Agent makes a mistake
 
 def test_artificial_failure():
     assert calculate_value() == 4
"""
    elif attempt == 2:
        # Agent realizes mistake from diagnostic and fixes it correctly
        return """diff --git a/tests/test_artificial.py b/tests/test_artificial.py
index e69de29..d95f3c9 100644
--- a/tests/test_artificial.py
+++ b/tests/test_artificial.py
@@ -1,5 +1,5 @@
 def calculate_value():
-    return 3 # BUG! Should return 4
+    return 4 # Agent fixes correctly
 
 def test_artificial_failure():
     assert calculate_value() == 4
"""
    return ""

def main():
    repo_path = "/root/projects/TheNovaNodes/antigravity-telegram-agent"
    loop = BoundedAutonomousLoop(repo_path)
    
    print("--- Starting First Vertical Slice POC ---")
    result = loop.run_cycle(dummy_agent)
    
    print("\n--- FINAL RESULT ---")
    print(f"Final State: {result['final_state']}")
    
    for entry in result['history']:
        print(f"Attempt {entry['attempt']}: {entry['status']}")
        if 'diagnostic' in entry:
            print("  [Diagnostic Extracted]")
            
    if result['final_state'] == 'CANDIDATE':
        print("\n[SUCCESS] Patch Candidate Extracted:")
        print(result['patch'])
    else:
        print("\n[FAILURE] Loop did not result in a candidate.")
        sys.exit(1)

if __name__ == "__main__":
    main()
