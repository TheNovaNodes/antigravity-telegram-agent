import subprocess
import hashlib
from typing import Dict, Any, List
from pathlib import Path

class VerificationEngine:
    """Runs protected tests to objectively verify patches."""
    
    def __init__(self, sandbox_path: str):
        self.sandbox_path = Path(sandbox_path)

    def verify(self) -> Dict[str, Any]:
        """Runs the test suite inside the sandbox and returns PASS/FAIL."""
        # Simple security invariant: check if protected tests were modified
        if self._protected_tests_modified():
            return {"status": "SECURITY_VIOLATION", "reason": "Protected tests were altered."}
        
        # Run pytest inside the sandbox
        res = subprocess.run(
            ["python3", "-m", "pytest", "tests/"],
            cwd=self.sandbox_path,
            capture_output=True,
            text=True
        )
        
        if res.returncode == 0:
            return {"status": "PASS", "output": res.stdout}
        else:
            return {"status": "FAIL", "output": res.stdout, "stderr": res.stderr}

    def _protected_tests_modified(self) -> bool:
        """Check if the agent manipulated the tests instead of fixing the code."""
        # In a real implementation, we'd hash the `tests/` directory pre-patch
        # and compare post-patch, or use `git status -- tests/`.
        res = subprocess.run(
            ["git", "diff", "--name-only", "main"],
            cwd=self.sandbox_path,
            capture_output=True,
            text=True
        )
        changed_files = res.stdout.splitlines()
        # For this prototype, any change to `tests/` except specific allowed files is a violation
        return any(f.startswith("tests/") for f in changed_files if "test_artificial" not in f)

class DiagnosticEngine:
    """Extracts fingerprints from failures to prevent loop stuckness."""
    
    @staticmethod
    def extract(test_output: str) -> Dict[str, Any]:
        """Extracts the core traceback and fingerprint."""
        lines = test_output.splitlines()
        traceback_lines = [l for l in lines if "E   " in l or "FAILED " in l]
        
        fingerprint_raw = "\n".join(traceback_lines)
        fingerprint_hash = hashlib.md5(fingerprint_raw.encode()).hexdigest()
        
        return {
            "fingerprint": fingerprint_hash,
            "relevant_lines": "\n".join(traceback_lines[:15]), # Don't overwhelm context
            "full_extracted": fingerprint_raw
        }
