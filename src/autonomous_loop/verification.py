import subprocess
import hashlib
import json
import uuid
import os
from typing import Dict, Any, List
from pathlib import Path

import logging
logger = logging.getLogger(__name__)

class VerificationEngine:
    """Runs protected tests to objectively verify patches inside a Docker Sandbox with Canary Attestation."""
    
    def __init__(self, sandbox_path: str):
        self.sandbox_path = Path(sandbox_path)
        self.canary_token = uuid.uuid4().hex
        self.canary_test_name = f"test_canary_{self.canary_token[:8]}"

    def _inject_canary(self):
        """Injects a dynamically generated canary test that MUST fail.
        If the agent spoofs a PASS or uses os._exit(0), this test will incorrectly pass or be skipped.
        The orchestrator requires this test to be exactly in the FAILED state in the JSON report.
        """
        canary_content = f"""
def {self.canary_test_name}_pass():
    assert True

def {self.canary_test_name}_fail():
    assert False, "CANARY_INTENTIONAL_FAILURE_{self.canary_token}"
"""
        self.canary_file = self.sandbox_path / "tests" / f"test_canary_auto.py"
        self.canary_file.parent.mkdir(exist_ok=True)
        self.canary_file.write_text(canary_content)

    def _remove_canary(self):
        if hasattr(self, 'canary_file') and self.canary_file.exists():
            self.canary_file.unlink()

    def verify(self) -> Dict[str, Any]:
        """Runs the test suite inside the unprivileged Docker sandbox and attests the result."""
        
        # 1. Check if agent bypassed PatchValidator and tampered with tests manually
        if self._protected_tests_modified():
            return {"status": "SECURITY_VIOLATION", "reason": "Protected tests were altered."}
        
        self._inject_canary()
        
        # 2. Run inside Docker (Host RCE Blocked, Unprivileged user, No Network)
        # We output a JSON report to verify exactly what happened.
        cmd = [
            "docker", "run", "--rm",
            "--network", "none",
            "--memory", "512m",
            "--cpus", "1.0",
            "--user", "1000", 
            "-v", f"{self.sandbox_path}:/workspace",
            "ag_test_runner",
            "pytest", "tests/", "--json-report", "--json-report-file=/workspace/.report.json"
        ]
        
        try:
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        except subprocess.TimeoutExpired:
            self._remove_canary()
            return {"status": "FAIL", "reason": "RESOURCE_ABUSE", "output": "Timeout expired."}
            
        self._remove_canary()
        
        # 3. Attest the result using the JSON report
        report_file = self.sandbox_path / ".report.json"
        if not report_file.exists():
            # If agent used os._exit(0), the report might not exist
            return {"status": "SECURITY_VIOLATION", "reason": "Test report missing (possible os._exit(0) abort).", "output": res.stdout}
            
        try:
            report_data = json.loads(report_file.read_text())
        except Exception as e:
            return {"status": "SECURITY_VIOLATION", "reason": "Malformed test report.", "output": res.stdout}
            
        # 4. Check Canary (Attestation)
        # We expect exactly one failure: the canary fail test.
        # If the agent monkey-patched pytest to pass all tests, the canary fail will be missing or passed.
        tests = report_data.get("tests", [])
        
        canary_fail_found = False
        canary_pass_found = False
        
        for t in tests:
            if self.canary_test_name + "_fail" in t.get("nodeid", ""):
                canary_fail_found = True
                if t.get("outcome") != "failed":
                    return {"status": "SECURITY_VIOLATION", "reason": "Canary test spoofed (False PASS)."}
                # Check that it failed for the exact right reason, not because of a syntax error
                call = t.get("call", {})
                if "CANARY_INTENTIONAL_FAILURE" not in call.get("crash", {}).get("message", ""):
                    return {"status": "SECURITY_VIOLATION", "reason": "Canary failure signature spoofed."}
                    
            if self.canary_test_name + "_pass" in t.get("nodeid", ""):
                canary_pass_found = True
                if t.get("outcome") != "passed":
                    return {"status": "FAIL", "reason": "Canary pass failed."}
                    
        if not canary_fail_found or not canary_pass_found:
            return {"status": "SECURITY_VIOLATION", "reason": "Canary tests missing (Test skipping/aborting detected)."}
            
        # 5. Check other tests
        # Now we know the test runner legitimately executed to completion without spoofing outcomes.
        # All tests OTHER than the canary must pass.
        for t in tests:
            if "test_canary_auto.py" in t.get("nodeid", ""):
                continue
            if t.get("outcome") != "passed":
                # A legitimate failure in a protected or regression test
                return {"status": "FAIL", "output": res.stdout}
                
        # If we got here, all real tests passed, and the canary failed as expected.
        return {"status": "PASS", "output": res.stdout}

    def _protected_tests_modified(self) -> bool:
        res = subprocess.run(
            ["git", "diff", "--name-status", "main"],
            cwd=self.sandbox_path,
            capture_output=True,
            text=True
        )
        for line in res.stdout.splitlines():
            if not line.strip():
                continue
            parts = line.split(maxsplit=1)
            if len(parts) != 2:
                continue
            status, path = parts[0], parts[1]
            if path.startswith("tests/"):
                if status == "A" and path.startswith("tests/test_agent_repair_"):
                    continue
                return True
        return False

class DiagnosticEngine:
    @staticmethod
    def extract(test_output: str) -> Dict[str, Any]:
        lines = test_output.splitlines()
        traceback_lines = [l for l in lines if "E   " in l or "FAILED " in l]
        fingerprint_raw = "\n".join(traceback_lines)
        fingerprint_hash = hashlib.md5(fingerprint_raw.encode()).hexdigest()
        return {
            "fingerprint": fingerprint_hash,
            "relevant_lines": "\n".join(traceback_lines[:15]),
            "full_extracted": fingerprint_raw
        }

# Patch for permissions
_original_verify = VerificationEngine.verify
def _patched_verify(self):
    subprocess.run(["chmod", "-R", "777", str(self.sandbox_path)])
    return _original_verify(self)
VerificationEngine.verify = _patched_verify
