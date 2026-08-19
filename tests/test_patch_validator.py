import unittest
import tempfile
import os
import shutil
from pathlib import Path
from src.autonomous_loop.patch_validator import PatchValidator, AgentPatch, PatchValidationError

class TestPatchValidator(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.mkdtemp()
        self.worktree = Path(self.temp_dir)
        self.validator = PatchValidator(self.worktree)
        
        # Create some files
        (self.worktree / "src").mkdir()
        (self.worktree / "src" / "example.py").write_text("def test():\n    return 1\n", encoding="utf-8")
        (self.worktree / "src" / "ambiguous.py").write_text("foo\nbar\nfoo\n", encoding="utf-8")
        
        (self.worktree / "tests").mkdir()
        (self.worktree / "tests" / "test_existing.py").write_text("import os\n", encoding="utf-8")
        
        (self.worktree / ".git").mkdir()
        (self.worktree / ".git" / "config").write_text("[core]\n", encoding="utf-8")
        
    def tearDown(self):
        shutil.rmtree(self.temp_dir, ignore_errors=True)
        # Also clean up backup if test failed and left it
        backup = self.worktree.parent / (self.worktree.name + "_backup")
        if backup.exists():
            shutil.rmtree(backup, ignore_errors=True)

    def test_valid_replacement(self):
        patch = AgentPatch("REPLACE_EXACT", "src/example.py", old_text="return 1", new_text="return 2")
        self.assertTrue(self.validator.apply_patch_atomic([patch]))
        self.assertIn("return 2", (self.worktree / "src" / "example.py").read_text())

    def test_missing_context(self):
        patch = AgentPatch("REPLACE_EXACT", "src/example.py", old_text="return 42", new_text="return 2")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "CONTEXT_NOT_FOUND")

    def test_ambiguous_context(self):
        patch = AgentPatch("REPLACE_EXACT", "src/ambiguous.py", old_text="foo\n", new_text="baz\n")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "AMBIGUOUS_CONTEXT")

    def test_protected_test_modification(self):
        patch = AgentPatch("REPLACE_EXACT", "tests/test_existing.py", old_text="import os", new_text="import sys")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "PROTECTED_TEST_MODIFIED")

    def test_new_test_creation_allowed(self):
        patch = AgentPatch("CREATE_FILE", "tests/test_agent_repair_123.py", new_text="def test_a(): pass")
        self.assertTrue(self.validator.apply_patch_atomic([patch]))
        self.assertTrue((self.worktree / "tests" / "test_agent_repair_123.py").exists())

    def test_new_test_creation_blocked(self):
        patch = AgentPatch("CREATE_FILE", "tests/test_something_else.py", new_text="def test_a(): pass")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "SECURITY_VIOLATION")

    def test_path_traversal(self):
        patch = AgentPatch("CREATE_FILE", "../escaped.py", new_text="bad")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "PATH_ESCAPE")

    def test_absolute_path(self):
        patch = AgentPatch("CREATE_FILE", "/etc/passwd", new_text="bad")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "PATH_ESCAPE")

    def test_git_modification(self):
        patch = AgentPatch("REPLACE_EXACT", ".git/config", old_text="[core]", new_text="[core]\nbad=true")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "SECURITY_VIOLATION")
        
    def test_symlink_escape(self):
        symlink_path = self.worktree / "link"
        os.symlink("/etc", symlink_path)
        patch = AgentPatch("CREATE_FILE", "link/passwd", new_text="bad")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "PATH_ESCAPE") # or similar

    def test_atomic_rollback(self):
        valid_patch = AgentPatch("REPLACE_EXACT", "src/example.py", old_text="return 1", new_text="return 2")
        invalid_patch = AgentPatch("REPLACE_EXACT", "src/example.py", old_text="missing", new_text="return 3")
        
        with self.assertRaises(PatchValidationError):
            self.validator.apply_patch_atomic([valid_patch, invalid_patch])
            
        # File should be rolled back to original state
        self.assertIn("return 1", (self.worktree / "src" / "example.py").read_text())

    def test_malformed_agent_response(self):
        patch = AgentPatch("DELETE_FILE", "src/example.py")
        with self.assertRaises(PatchValidationError) as context:
            self.validator.apply_patch_atomic([patch])
        self.assertEqual(context.exception.failure_type, "POLICY_BYPASS")
        
if __name__ == '__main__':
    unittest.main()
