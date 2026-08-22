import unittest
import tempfile
import os
import hashlib
from pathlib import Path
from unittest.mock import patch, MagicMock

from src.autonomous_loop.verification import VerificationEngine, DiagnosticEngine, ALLOWED_SANDBOX_ROOTS


class TestVerifierSecurity(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
