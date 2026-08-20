import os
import shutil
import ast
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

class SecurityASTVisitor(ast.NodeVisitor):
    def __init__(self):
        self.forbidden_imports = {
            "os", "sys", "subprocess", "ctypes", "pty", "builtins", "inspect", "importlib",
            "pathlib", "shutil", "io", "socket", "urllib", "pickle", "marshal"
        }
        self.forbidden_calls = {
            "eval", "exec", "open", "globals", "locals", "getattr", "setattr", "delattr", 
            "__import__", "compile", "exit", "quit"
        }

    def visit_Import(self, node):
        for alias in node.names:
            base_module = alias.name.split('.')[0]
            if base_module in self.forbidden_imports:
                raise Exception(f"Importing {base_module} is forbidden.")
        self.generic_visit(node)

    def visit_ImportFrom(self, node):
        if node.module:
            base_module = node.module.split('.')[0]
            if base_module in self.forbidden_imports:
                raise Exception(f"Importing from {base_module} is forbidden.")
        self.generic_visit(node)

    def visit_Call(self, node):
        if isinstance(node.func, ast.Name) and node.func.id in self.forbidden_calls:
            raise Exception(f"Calling {node.func.id} is forbidden.")
        if isinstance(node.func, ast.Attribute) and node.func.attr in self.forbidden_calls:
            raise Exception(f"Calling {node.func.attr} is forbidden.")
        self.generic_visit(node)
        
    def visit_Attribute(self, node):
        if node.attr in {"__class__", "__subclasses__", "__bases__", "__mro__", "__dict__", "__builtins__"}:
            raise Exception(f"Accessing dunder attribute {node.attr} is forbidden.")
        self.generic_visit(node)
        
    def visit_Name(self, node):
        if node.id == "__builtins__":
            raise Exception("Accessing __builtins__ is forbidden.")
        self.generic_visit(node)

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
        
        # Path traversal check using is_relative_to
        if not full_path.is_relative_to(self.worktree_path):
            raise PatchValidationError("PATH_ESCAPE", rel_path, "Path traversal forbidden.")
            
        if full_path.exists() and full_path.is_symlink():
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Symlinks are forbidden.")

        # SCOPE BOUNDARY (Deny by default)
        try:
            rel_resolved = full_path.relative_to(self.worktree_path)
        except ValueError:
            raise PatchValidationError("PATH_ESCAPE", rel_path, "Path escape detected.")
            
        parts = rel_resolved.parts
        if not parts:
            raise PatchValidationError("INVALID_PATH", rel_path, "Invalid path.")

        FORBIDDEN_EXTS = {".ini", ".cfg", ".toml", ".env"}
        if full_path.suffix in FORBIDDEN_EXTS or full_path.name in FORBIDDEN_EXTS:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"Configuration files ({full_path.suffix or full_path.name}) are forbidden.")

        FORBIDDEN_FILES = {"conftest.py", "sitecustomize.py", "usercustomize.py"}
        if full_path.name in FORBIDDEN_FILES or full_path.name.startswith("Dockerfile"):
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"{full_path.name} modification is forbidden.")

        FORBIDDEN_DIRS = {".git", ".github", "scripts"}
        if parts[0] in FORBIDDEN_DIRS:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, f"{parts[0]} modification is forbidden.")

        is_allowed = False

        if parts[0] == "src":
            if len(parts) > 1 and parts[1] == "autonomous_loop":
                raise PatchValidationError("SECURITY_VIOLATION", rel_path, "src/autonomous_loop/ is IMMUTABLE.")
            is_allowed = True
            
        elif parts[0] == "tests":
            if operation == "CREATE_FILE":
                if not full_path.name.startswith("test_agent_repair_"):
                    raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Agent can only create tests matching test_agent_repair_*.")
                is_allowed = True
            else:
                raise PatchValidationError("PROTECTED_TEST_MODIFIED", rel_path, "Existing tests are immutable.")

        if not is_allowed:
            raise PatchValidationError("SECURITY_VIOLATION", rel_path, "Path is not explicitly ALLOWED by Agent Write Scope.")

    def _validate_ast_security(self, content: str, path: str):
        if not path.endswith(".py"):
            return
        try:
            tree = ast.parse(content)
            visitor = SecurityASTVisitor()
            visitor.visit(tree)
        except SyntaxError as e:
            raise PatchValidationError("INVALID_PATCH", path, f"SyntaxError in patch: {e}")
        except Exception as e:
            raise PatchValidationError("SECURITY_VIOLATION", path, f"Malicious AST payload detected: {e}")

    def validate_patch(self, patch: AgentPatch):
        if patch.operation not in self.ALLOWED_OPERATIONS:
            raise PatchValidationError("POLICY_BYPASS", patch.path, f"Operation {patch.operation} is forbidden.")
            
        self._validate_path_security(patch.path, patch.operation)

        full_path = self.worktree_path / patch.path
        
        if patch.operation == "CREATE_FILE":
            if full_path.exists():
                raise PatchValidationError("FILE_EXISTS", patch.path, "Cannot create file that already exists.")
            if not patch.new_text:
                raise PatchValidationError("INVALID_PATCH", patch.path, "CREATE_FILE requires new_text.")
            self._validate_ast_security(patch.new_text, patch.path)
                
        else:
            if not full_path.exists():
                raise PatchValidationError("FILE_NOT_FOUND", patch.path, "Target file does not exist.")
            if patch.old_text is None:
                raise PatchValidationError("INVALID_PATCH", patch.path, "Operation requires old_text.")
                
            content = full_path.read_text(encoding="utf-8")
            occurrences = content.count(patch.old_text)
            if occurrences == 0:
                raise PatchValidationError("CONTEXT_NOT_FOUND", patch.path, "old_text not found.")
            elif occurrences > 1:
                raise PatchValidationError("AMBIGUOUS_CONTEXT", patch.path, "Ambiguous old_text.")
                
            new_content = content
            if patch.operation == "REPLACE_EXACT":
                new_content = content.replace(patch.old_text, patch.new_text)
            elif patch.operation == "INSERT_AFTER":
                new_content = content.replace(patch.old_text, patch.old_text + "\n" + patch.new_text)
            elif patch.operation == "INSERT_BEFORE":
                new_content = content.replace(patch.old_text, patch.new_text + "\n" + patch.old_text)
            self._validate_ast_security(new_content, patch.path)

    def apply_patch_atomic(self, patches: List[AgentPatch]) -> bool:
        backup_dir = self.worktree_path.parent / (self.worktree_path.name + "_backup")
        if backup_dir.exists():
            shutil.rmtree(backup_dir)
        shutil.copytree(self.worktree_path, backup_dir, symlinks=True)
        
        try:
            for patch in patches:
                self.validate_patch(patch)
            for patch in patches:
                self._apply_single(patch)
            shutil.rmtree(backup_dir)
            return True
        except PatchValidationError as e:
            self._rollback(backup_dir)
            raise e
        except Exception as e:
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
        shutil.copytree(backup_dir, self.worktree_path, symlinks=True)
        shutil.rmtree(backup_dir)
