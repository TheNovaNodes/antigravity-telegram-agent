import unittest
import tempfile
import shutil
from pathlib import Path
from src.autonomous_loop.patch_validator import PatchValidator, AgentPatch, PatchValidationError

class TestPatchValidatorStrictScope(unittest.TestCase):
    def setUp(self):
        self.worktree = Path(tempfile.mkdtemp())
        
        # Create some files for test context
        (self.worktree / "src").mkdir()
        (self.worktree / "src" / "product.py").write_text("def run(): pass")
        (self.worktree / "src" / "autonomous_loop").mkdir()
        (self.worktree / "src" / "autonomous_loop" / "verification.py").write_text("def verify(): pass")
        
        self.validator = PatchValidator(self.worktree)

    def tearDown(self):
        shutil.rmtree(self.worktree, ignore_errors=True)

    def test_allow_product_code(self):
        # src/product.py should be allowed
        patch = AgentPatch("REPLACE_EXACT", "src/product.py", "pass", "return True")
        self.validator.validate_patch(patch) # should not raise

    def test_deny_autonomous_loop(self):
        patch = AgentPatch("REPLACE_EXACT", "src/autonomous_loop/verification.py", "pass", "return True")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("IMMUTABLE", str(cm.exception))
        self.assertEqual(cm.exception.failure_type, "SECURITY_VIOLATION")

    def test_deny_conftest(self):
        patch = AgentPatch("CREATE_FILE", "conftest.py", new_text="def pytest_runtest_makereport(): pass")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("conftest.py modification is forbidden", str(cm.exception))

    def test_deny_pytest_ini(self):
        patch = AgentPatch("CREATE_FILE", "pytest.ini", new_text="[pytest]")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("Configuration files (.ini) are forbidden", str(cm.exception))

    def test_deny_pyproject_toml(self):
        patch = AgentPatch("CREATE_FILE", "pyproject.toml", new_text="[tool.poetry]")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("Configuration files (.toml) are forbidden", str(cm.exception))

    def test_deny_github_workflow(self):
        patch = AgentPatch("CREATE_FILE", ".github/workflows/build.yml", new_text="on: push")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn(".github modification is forbidden", str(cm.exception))
        
    def test_deny_env_file(self):
        patch = AgentPatch("CREATE_FILE", ".env", new_text="SECRET=123")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("Configuration files (.env) are forbidden", str(cm.exception))
        
    def test_deny_sitecustomize(self):
        patch = AgentPatch("CREATE_FILE", "sitecustomize.py", new_text="import os")
        with self.assertRaises(PatchValidationError) as cm:
            self.validator.validate_patch(patch)
        self.assertIn("sitecustomize.py modification is forbidden", str(cm.exception))

    def test_allow_new_regression_test(self):
        patch = AgentPatch("CREATE_FILE", "tests/test_agent_repair_123.py", new_text="def test_fix(): pass")
        # Should not raise exception
        self.validator.validate_patch(patch)

if __name__ == "__main__":
    unittest.main()
