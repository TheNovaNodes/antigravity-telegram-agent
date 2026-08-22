import subprocess
import hashlib
import json
import os
import uuid
import tempfile
from typing import Dict, Any, List, Optional
from pathlib import Path
import logging

logger = logging.getLogger(__name__)

def get_allowed_sandbox_roots() -> List[Path]:
    """Safely return default allowlisted root directories for sandbox paths without failing on import."""
    candidates = [Path("/tmp"), Path.home(), Path("/root/projects")]
    roots = []
    for candidate in candidates:
        try:
            if candidate.exists():
                roots.append(candidate.resolve())
        except (PermissionError, OSError) as e:
            logger.debug(f"Skipping sandbox root candidate {candidate} due to permission/OS error: {e}")
    if not roots:
        roots.append(Path.home().resolve() if try_resolve_home() else Path("/tmp"))
    return roots


def try_resolve_home() -> bool:
    try:
        return Path.home().exists()
    except (PermissionError, OSError):
        return False


class VerificationEngine:
    """Runs protected tests to objectively verify patches inside a Docker Sandbox.
    Enforces Phase 2 Clean Environment and prevents output/disk exhaustion."""
    
    def __init__(self, sandbox_path: str, allowed_roots: Optional[List[Path]] = None):
        raw_path = Path(sandbox_path)

        # Symlink escape check
        if raw_path.is_symlink():
            raise ValueError(f"Symlink sandbox path rejected: '{raw_path}' is a symlink")

        # Canonicalize path (.resolve())
        canonical_path = raw_path.resolve()

        if canonical_path.is_symlink():
            raise ValueError(f"Symlink sandbox path rejected: '{canonical_path}' is a symlink")

        # Validate owned/allowlisted root
        roots = allowed_roots if allowed_roots is not None else get_allowed_sandbox_roots()
        is_allowlisted = False
        for root in roots:
            try:
                canonical_path.relative_to(root.resolve())
                is_allowlisted = True
                break
            except ValueError:
                pass

        if not is_allowlisted:
            raise ValueError(f"Sandbox path '{canonical_path}' is outside allowlisted roots: {roots}")

        self.sandbox_path: Path = canonical_path

    def verify(self, trusted_manifest: Dict[str, Any] = None) -> Dict[str, Any]:
        """Runs Phase 2 Verification with strictly bounded resources and trusted manifest."""
        
        # 1. ENFORCE CLEAN VERIFICATION ENVIRONMENT
        tests_dir = self.sandbox_path / "tests"
        if tests_dir.exists():
            for p in tests_dir.glob("test_agent_repair_*.py"):
                p.unlink()

        # Remove os.system()! Use os.chmod or subprocess.run(shell=False)
        try:
            os.chmod(self.sandbox_path, 0o755)
        except Exception as e:
            logger.debug(f"Failed to chmod sandbox_path: {e}")

        # 2. ISOLATED, READ-ONLY, RESOURCE-BOUNDED EXECUTION
        fd, temp_out = tempfile.mkstemp()
        os.close(fd)
        
        cmd = [
            "docker", "run", "--rm",
            "--network", "none",
            "--memory", "512m",
            "--cpus", "1.0",
            "--pids-limit", "100",
            "--log-driver=none",
            "--read-only",
            "--tmpfs", "/tmp:rw,size=50m,mode=1777",
            "--tmpfs", "/run:rw,size=10m,mode=1777",
            "--user", "1000",
            "-e", "PYTHONDONTWRITEBYTECODE=1", "-e", "TELEGRAM_BOT_TOKEN=dummy", "-e", "ALLOWED_USER_IDS=123", "-e", "AG_TEST_DB_PATH=/tmp/ag.db",
            "-v", f"{self.sandbox_path}:/workspace:ro",
            "-w", "/workspace",
            "ag_test_runner",
            "bash", "-c", "pytest tests/ -p no:cacheprovider --json-report --json-report-file=/tmp/report.json > /tmp/out.log 2>&1; cat /tmp/report.json; echo '---STDOUT---'; head -c 100000 /tmp/out.log"
        ]
        
        try:
            with open(temp_out, "w") as f_out:
                res = subprocess.run(cmd, stdout=f_out, stderr=subprocess.STDOUT, timeout=30, shell=False)
            with open(temp_out, "r") as f_in:
                output_str = f_in.read(1024 * 256)
        except subprocess.TimeoutExpired:
            os.unlink(temp_out)
            return {"status": "FAIL", "reason": "RESOURCE_ABUSE", "output": "Timeout expired."}
        except Exception as e:
            if os.path.exists(temp_out):
                os.unlink(temp_out)
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": f"Verifier exception: {e}", "output": ""}
        finally:
            if os.path.exists(temp_out):
                os.unlink(temp_out)

        # 3. SPLIT STDOUT AND REPORT (For diagnostics only)
        parts = output_str.split("---STDOUT---", 1)
        json_part = parts[0].strip() if len(parts) == 2 else ""
        stdout_part = parts[1].strip() if len(parts) == 2 else output_str
        
        report_data = None
        if json_part:
            try:
                report_data = json.loads(json_part)
            except Exception:
                pass

        # 4. TRUSTED PARENT VERIFICATION LOGIC (Fail-Closed)
        
        # V-001: Exit non-zero -> FAIL
        if res.returncode != 0:
            return {"status": "FAIL", "reason": "Test process returned non-zero exit code.", "output": stdout_part}
            
        # V-002, V-003: No trusted manifest -> INCONCLUSIVE
        if not trusted_manifest:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Missing trusted manifest.", "output": stdout_part}
            
        expected_tests = set(trusted_manifest.get("expected_nodeids", []))
        if not expected_tests:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Empty expected tests in manifest.", "output": stdout_part}
            
        if not report_data:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "No report returned from exit 0 process.", "output": stdout_part}
            
        # V-004, V-005: Validate Collection
        tests_run = report_data.get("tests", [])
        if not tests_run:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "No tests executed in report.", "output": stdout_part}
            
        actual_tests = set(t.get("nodeid") for t in tests_run)
        missing_tests = expected_tests - actual_tests
        unexpected_tests = actual_tests - expected_tests
        
        if missing_tests or unexpected_tests:
            return {"status": "FAIL", "reason": f"Manifest node ID mismatch. Missing: {missing_tests}, Unexpected: {unexpected_tests}", "output": stdout_part}

        # We must ensure all base test files were executed
        expected_files = set(node.split("::")[0] for node in expected_tests)
        actual_files = set(node.split("::")[0] for node in actual_tests)
        
        missing_files = expected_files - actual_files
        if missing_files:
            return {"status": "FAIL", "reason": f"Required test files skipped: {missing_files}", "output": stdout_part}

        # Ensure NO test failed
        for t in tests_run:
            if t.get("outcome") != "passed":
                # Contradiction: exit 0 but report says failure -> INCONCLUSIVE
                return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Contradictory signals: exit 0 but report indicates failure.", "output": stdout_part}
                
        # If we got here: exit is 0, manifest exists, report says pass.
        # But V-005 check: did it actually run tests?
        if len(actual_tests) == 0:
            return {"status": "VERIFICATION_INCONCLUSIVE", "reason": "Zero tests executed.", "output": stdout_part}
            
        return {"status": "PASS", "output": stdout_part}

class DiagnosticEngine:
    @staticmethod
    def extract(test_output: str) -> Dict[str, Any]:
        lines = test_output.splitlines()
        traceback_lines = [l for l in lines if "E   " in l or "FAILED " in l]
        fingerprint_raw = "\n".join(traceback_lines)
        fingerprint_hash = hashlib.sha256(fingerprint_raw.encode(), usedforsecurity=False).hexdigest()
        return {
            "fingerprint": fingerprint_hash,
            "relevant_lines": "\n".join(traceback_lines[:15]),
            "full_extracted": fingerprint_raw
        }
