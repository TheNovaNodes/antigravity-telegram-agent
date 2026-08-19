import os
import shutil
import hashlib
from typing import List, Dict, Any, Optional
from dataclasses import dataclass
from pathlib import Path
import logging

logger = logging.getLogger(__name__)

@dataclass
class AgentPatch:
    operation: str
    path: str
    old_text: Optional[str] = None
    new_text: Optional[str] = None
    metadata: Optional[Dict[str, Any]] = None

class PatchValidationError(Exception):
    def __init__(self, failure_type: str, path: str, message: str):
        self.failure_type = failure_type
        self.path = path
        super().__init__(message)
        
    def __str__(self):
        return f"{self.failure_type} on {self.path}: {super().__str__()}"

class PatchValidator:
    ALLOWED_OPERATIONS = {"CREATE_FILE", "REPLACE_EXACT", "INSERT_AFTER", "INSERT_BEFORE"}
    FORBIDDEN_OPERATIONS = {"DELETE_FILE", "RENAME_FILE", "MOVE_FILE", "BINARY_MODIFICATION"}

    def __init__(self, worktree_path: Path):
        self.worktree_path = worktree_path.resolve()

    def _validate_path_security(self, rel_path: str, operation: str):
        if not rel_path:
            raise PatchValidationError("INVALID_PATH", rel_path, "Path cannot be empty.")
            
        if os.path.isabs(rel_path):
            raise PatchValidationError("PATH_ESCAPE", rel_path, "Absolute paths are forbidden.")
            
        full_path = (self.worktree_path / rel_path).resolve()
        
        # Path traversal check
        if not str(full_path).startswith(str(self.worktree_path)):
            raise PatchValidationError("PATH_ESCAPE", rel_path, "Path traversal forbidden.")
            
        if full_path.exists() and full_path.is_symlink():
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Symlinks are forbidden.")

        # SCOPE BOUNDARY (Deny by default)
        p = Path(rel_path)
        parts = p.parts

        FORBIDDEN_EXTS = {".ini", ".cfg", ".toml", ".env"}
        if p.suffix in FORBIDDEN_EXTS or p.name in FORBIDDEN_EXTS:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"Configuration files ({p.suffix or p.name}) are forbidden.")

        FORBIDDEN_FILES = {"conftest.py", "sitecustomize.py", "usercustomize.py"}
        if p.name in FORBIDDEN_FILES or p.name.startswith("Dockerfile") or p.name.startswith("docker-compose"):
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"{p.name} modification is forbidden.")

        FORBIDDEN_DIRS = {".git", ".github", "scripts"}
        if parts[0] in FORBIDDEN_DIRS:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"{parts[0]} modification is forbidden.")

        # Allowed rules
        is_allowed = False

        if parts[0] == "src":
            if len(parts) > 1 and parts[1] == "autonomous_loop":
                raise PatchValidationError("SECURITY_VIOLATION", rel_path, "src/autonomous_loop/ is IMMUTABLE.")
            is_allowed = True
            
        elif parts[0] == "tests":
            if operation == "CREATE_FILE":
                if not p.name.startswith("test_agent_repair_"):
                    raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Agent can only create tests matching test_agent_repair_*.")
                is_allowed = True
            else:
                raise PatchValidationError("PROTECTED_TEST_MODIFIED", rel_path, "Existing tests are immutable.")

        if not is_allowed:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Path is not explicitly ALLOWED by Agent Write Scope.")

    def validate_patch(self, patch: AgentPatch):
        if patch.operation not in self.ALLOWED_OPERATIONS:
            if patch.operation in self.FORBIDDEN_OPERATIONS:
                raise PatchValidationError("POLICY_BYPASS", patch.path, f"Operation {patch.operation} is forbidden.")
            raise PatchValidationError("INVALID_OPERATION", patch.path, f"Unknown operation {patch.operation}.")
            
        self._validate_path_security(patch.path, patch.operation)

        full_path = self.worktree_path / patch.path
        
        if patch.operation == "CREATE_FILE":
            if full_path.exists():
                raise PatchValidationError("FILE_EXISTS", patch.path, "Cannot create file that already exists.")
            if not patch.new_text:
                raise PatchValidationError("INVALID_PATCH", patch.path, "CREATE_FILE requires new_text.")
                
        else: # REPLACE_EXACT, INSERT_AFTER, INSERT_BEFORE
            if not full_path.exists():
                raise PatchValidationError("FILE_NOT_FOUND", patch.path, "Target file does not exist.")
            if patch.old_text is None:
                raise PatchValidationError("INVALID_PATCH", patch.path, "Operation requires old_text context.")
                
            content = full_path.read_text(encoding="utf-8")
            
            # Context Uniqueness Check
            occurrences = content.count(patch.old_text)
            if occurrences == 0:
                raise PatchValidationError("CONTEXT_NOT_FOUND", patch.path, "The old_text was not found in the file.")
            elif occurrences > 1:
                raise PatchValidationError("AMBIGUOUS_CONTEXT", patch.path, f"Found {occurrences} occurrences of old_text. Must be exactly 1.")

    def apply_patch_atomic(self, patches: List[AgentPatch]) -> bool:
        """Validates and applies a list of patches atomically. Rolls back on error."""
        # 1. Snapshot / Backup
        backup_dir = self.worktree_path.parent / (self.worktree_path.name + "_backup")
        if backup_dir.exists():
            shutil.rmtree(backup_dir)
        shutil.copytree(self.worktree_path, backup_dir)
        
        try:
            # 2. Validate all first
            for patch in patches:
                self.validate_patch(patch)
                
            # 3. Apply all
            for patch in patches:
                self._apply_single(patch)
                
            shutil.rmtree(backup_dir)
            return True
            
        except PatchValidationError as e:
            logger.error(f"Patch validation failed for {e.path}: {e.failure_type} - {e}")
            self._rollback(backup_dir)
            raise e
        except Exception as e:
            logger.error(f"Patch application failed: {e}")
            self._rollback(backup_dir)
            raise PatchValidationError("APPLY_ERROR", "unknown", str(e))

    def _apply_single(self, patch: AgentPatch):
        full_path = self.worktree_path / patch.path
        if patch.operation == "CREATE_FILE":
            full_path.parent.mkdir(parents=True, exist_ok=True)
            full_path.write_text(patch.new_text, encoding="utf-8")
        else:
            content = full_path.read_text(encoding="utf-8")
            if patch.operation == "REPLACE_EXACT":
                content = content.replace(patch.old_text, patch.new_text)
            elif patch.operation == "INSERT_AFTER":
                content = content.replace(patch.old_text, patch.old_text + "\n" + patch.new_text)
            elif patch.operation == "INSERT_BEFORE":
                content = content.replace(patch.old_text, patch.new_text + "\n" + patch.old_text)
            full_path.write_text(content, encoding="utf-8")

    def _rollback(self, backup_dir: Path):
        shutil.rmtree(self.worktree_path)
        shutil.copytree(backup_dir, self.worktree_path)
        shutil.rmtree(backup_dir)
