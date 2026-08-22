import unittest
import tempfile
import os
import hashlib
import json
from pathlib import Path
from unittest.mock import patch, MagicMock

import sys
import importlib

from src.autonomous_loop.verification import VerificationEngine, DiagnosticEngine, get_allowed_sandbox_roots


class TestVerifierSecurity(unittest.TestCase):
    def test_import_safety_with_permission_error(self):
        """Test that importing verification module and resolving roots safely catches PermissionError/OSError."""
        with patch.object(Path, "exists", side_effect=PermissionError("Permission denied")):
            roots = get_allowed_sandbox_roots()
            self.assertIsInstance(roots, list)
            # Ensure importing or re-importing the module raises no exceptions
            if "src.autonomous_loop.verification" in sys.modules:
                importlib.reload(sys.modules["src.autonomous_loop.verification"])
            else:
                importlib.import_module("src.autonomous_loop.verification")
    def setUp(self):
        self.tmp_dir = tempfile.TemporaryDirectory()
        self.tmp_path = Path(self.tmp_dir.name).resolve()

    def tearDown(self):
        self.tmp_dir.cleanup()

    def test_sandbox_path_canonicalization_and_allowlisting(self):
        """Test sandbox_path is canonicalized (.resolve()) and validated against allowlist."""
        allowed_root = self.tmp_path / "allowed"
        allowed_root.mkdir(parents=True, exist_ok=True)
        valid_sandbox = allowed_root / "sandbox"
        valid_sandbox.mkdir(parents=True, exist_ok=True)

        engine = VerificationEngine(str(valid_sandbox), allowed_roots=[allowed_root])
        self.assertEqual(engine.sandbox_path, valid_sandbox.resolve())

        # Disallowed root
        disallowed_root = self.tmp_path / "disallowed"
        disallowed_root.mkdir(parents=True, exist_ok=True)
        with self.assertRaises(ValueError):
            VerificationEngine(str(disallowed_root), allowed_roots=[allowed_root])

    def test_symlink_escape_rejected(self):
        """Test symlinks pointing outside allowed root or inside are rejected."""
        allowed_root = self.tmp_path / "allowed"
        allowed_root.mkdir(parents=True, exist_ok=True)

        target_dir = self.tmp_path / "target"
        target_dir.mkdir(parents=True, exist_ok=True)

        symlink_path = allowed_root / "symlink_sandbox"
        os.symlink(target_dir, symlink_path)

        with self.assertRaises(ValueError):
            VerificationEngine(str(symlink_path), allowed_roots=[allowed_root])

    def test_diagnostic_engine_sha256_fingerprint(self):
        """Test DiagnosticEngine uses sha256 fingerprinting with usedforsecurity=False."""
        output_sample = "E   AssertionError: 1 != 2\nFAILED tests/test_foo.py::test_bar"
        res = DiagnosticEngine.extract(output_sample)

        expected_hash = hashlib.sha256(output_sample.encode(), usedforsecurity=False).hexdigest()
        self.assertEqual(res["fingerprint"], expected_hash)
        self.assertIn("AssertionError", res["relevant_lines"])


    def test_f04_verification_manifest_nodeid_mismatch(self):
        """Test F-04: missing_tests or unexpected_tests causes verification to return FAIL."""
        allowed_root = self.tmp_path / "allowed"
        allowed_root.mkdir(parents=True, exist_ok=True)
        valid_sandbox = allowed_root / "sandbox"
        valid_sandbox.mkdir(parents=True, exist_ok=True)

        engine = VerificationEngine(str(valid_sandbox), allowed_roots=[allowed_root])

        # Mock subprocess.run to return exit code 0 and valid report json
        mock_res = MagicMock()
        mock_res.returncode = 0

        report_content = json.dumps({
            "tests": [
                {"nodeid": "tests/test_foo.py::test_a", "outcome": "passed"},
                {"nodeid": "tests/test_foo.py::test_unexpected", "outcome": "passed"}
            ]
        })
        output_data = f"{report_content}\n---STDOUT---\nall good"

        trusted_manifest = {
            "expected_nodeids": ["tests/test_foo.py::test_a", "tests/test_foo.py::test_missing"]
        }

        with patch("subprocess.run", return_value=mock_res), \
             patch("builtins.open", unittest.mock.mock_open(read_data=output_data)):
            res = engine.verify(trusted_manifest=trusted_manifest)

        self.assertEqual(res["status"], "FAIL")
        self.assertIn("Manifest node ID mismatch", res["reason"])
        self.assertIn("missing", res["reason"])
        self.assertIn("unexpected", res["reason"])


if __name__ == "__main__":
    unittest.main()
