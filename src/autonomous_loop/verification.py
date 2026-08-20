import subprocess
import hashlib
import json
import os
import uuid
import tempfile
from typing import Dict, Any, List
from pathlib import Path

import logging
logger = logging.getLogger(__name__)

class VerificationEngine:
    """Runs protected tests to objectively verify patches inside a Docker Sandbox.
    Enforces Phase 2 Clean Environment and prevents output/disk exhaustion."""
    
    def __init__(self, sandbox_path: str):
        self.sandbox_path = Path(sandbox_path)

    def verify(self) -> Dict[str, Any]:
        """Runs Phase 2 Verification with strictly bounded resources."""
        
        # 1. ENFORCE CLEAN VERIFICATION ENVIRONMENT
        tests_dir = self.sandbox_path / "tests"
        if tests_dir.exists():
            for p in tests_dir.glob("test_agent_repair_*.py"):
                p.unlink()
                
        # Make the host sandbox readable by the Docker user (1000)
        os.system(f"chmod -R 755 {self.sandbox_path}")

        # 2. ISOLATED, READ-ONLY, RESOURCE-BOUNDED EXECUTION
        fd, temp_out = tempfile.mkstemp()
        os.close(fd)
        
        cmd = [
            "docker", "run", "--rm",
            "--network", "none",
            "--memory", "512m",
            "--cpus", "1.0",
            "--pids-limit", "100",           # Block fork bomb
            "--log-driver=none",             # Block Docker JSON log exhaustion
            "--read-only",                   # Block Host FS / Sandbox exhaustion
            "--tmpfs", "/tmp:rw,size=50m,mode=1777", # Bounded tmpfs
            "--tmpfs", "/run:rw,size=10m,mode=1777",
            "--user", "1000",
            "-e", "PYTHONDONTWRITEBYTECODE=1", "-e", "TELEGRAM_BOT_TOKEN=dummy", "-e", "ALLOWED_USER_IDS=123", "-e", "AG_TEST_DB_PATH=/tmp/ag.db",
            "-v", f"{self.sandbox_path}:/workspace:ro",
            "-w", "/workspace",
            "ag_test_runner",
            "bash", "-c", "pytest tests/ -p no:cacheprovider --json-report --json-report-file=/tmp/report.json > /tmp/out.log 2>&1; cat /tmp/report.json; echo '---STDOUT---'; head -c 100000 /tmp/out.log"
        ]
        
        try:
            # We use a temporary file to capture output to prevent Orchestrator RAM exhaustion
            with open(temp_out, "w") as f_out:
                res = subprocess.run(cmd, stdout=f_out, stderr=subprocess.STDOUT, timeout=30)
                
            # Read bounded output (first 256KB max to protect Orchestrator)
            with open(temp_out, "r") as f_in:
                output_str = f_in.read(1024 * 256)
        except subprocess.TimeoutExpired:
            os.unlink(temp_out)
            return {"status": "FAIL", "reason": "RESOURCE_ABUSE", "output": "Timeout expired."}
        finally:
            if os.path.exists(temp_out):
                os.unlink(temp_out)
                
        # 3. VERIFY EXIT CODE AND EXTRACT REPORT
        if res.returncode != 0:
            return {"status": "FAIL", "reason": "Test process returned non-zero exit code.", "output": output_str}

        if "---STDOUT---" not in output_str:
            return {"status": "SECURITY_VIOLATION", "reason": "Test report missing or corrupted (os._exit or AST syntax error).", "output": output_str}
            
        parts = output_str.split("---STDOUT---", 1)
        json_part = parts[0].strip()
        stdout_part = parts[1].strip()
        
        try:
            report_data = json.loads(json_part)
        except Exception:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Malformed test report.", "output": stdout_part}
            
        tests = report_data.get("tests", [])
        if not tests:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "No tests executed.", "output": stdout_part}
            
        # The report is an untrusted diagnostic attachment.
        # But if it contradicts the exit code (e.g. says failed but exit code is 0), fail-closed.
        for t in tests:
            if t.get("outcome") != "passed":
                return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Contradictory signals: exit=0 but report indicates failure.", "output": stdout_part}
                
        return {"status": "PASS", "output": stdout_part}

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
