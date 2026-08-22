import os
import stat
import sys
import subprocess
import tempfile
import importlib
from pathlib import Path
import pytest

from src.mcp_config import MCPConfigManager, DEFAULT_MCP_CONFIG_PATH
from src.db import init_db, DB_PATH, reset_db_connection


def test_import_config_and_mcp_config_zero_filesystem_side_effects(tmp_path, monkeypatch):
    """Verify that importing src.config and src.mcp_config produces zero filesystem write side-effects."""
    # Set CWD to temporary directory so any unintended file creation happens inside tmp_path
    monkeypatch.chdir(tmp_path)

    # Reload modules to test import side effects
    if "src.config" in sys.modules:
        importlib.reload(sys.modules["src.config"])
    else:
        importlib.import_module("src.config")

    if "src.mcp_config" in sys.modules:
        importlib.reload(sys.modules["src.mcp_config"])
    else:
        importlib.import_module("src.mcp_config")

    # Assert no files were created in CWD
    created_files = list(tmp_path.iterdir())
    assert len(created_files) == 0, f"Importing src.config / src.mcp_config created side-effect files: {created_files}"


def test_mcp_config_save_permissions(tmp_path):
    """Verify explicit saving of MCP config sets strict file permissions (0600)."""
    target_path = tmp_path / "test_mcp_config.json"
    mgr = MCPConfigManager(config_path=target_path)
    assert not target_path.exists()

    success = mgr.save_config()
    assert success is True
    assert target_path.exists()

    file_stat = target_path.stat()
    permissions = stat.S_IMODE(file_stat.st_mode)
    assert permissions == 0o600, f"Expected 0600 permissions on config file, got {oct(permissions)}"


def test_db_generated_files_permissions(tmp_path, monkeypatch):
    """Verify database and state files under data directory get strict 0600 permissions."""
    test_db = tmp_path / "data" / "test_agent.db"
    monkeypatch.setattr("src.db.DB_PATH", test_db)
    reset_db_connection()

    try:
        init_db()
        assert test_db.exists()

        file_stat = test_db.stat()
        permissions = stat.S_IMODE(file_stat.st_mode)
        assert permissions == 0o600, f"Expected 0600 permissions on DB file, got {oct(permissions)}"

        wal_file = tmp_path / "data" / "test_agent.db-wal"
        if wal_file.exists():
            wal_stat = wal_file.stat()
            wal_permissions = stat.S_IMODE(wal_stat.st_mode)
            assert wal_permissions == 0o600, f"Expected 0600 permissions on WAL file, got {oct(wal_permissions)}"
    finally:
        reset_db_connection()



def test_repository_hygiene_no_tracked_poc_scripts():
    """Verify repository hygiene: scratch/PoC scripts reading credentials are untracked and in .gitignore."""
    repo_root = Path(__file__).resolve().parent.parent
    banned_pocs = ["first_slice_demo.py", "translate.py", "translate_all.py", "test_bot.py"]

    # Check git tracked files
    result = subprocess.run(
        ["git", "ls-files"] + banned_pocs,
        cwd=repo_root,
        capture_output=True,
        text=True,
        check=True
    )
    tracked_files = [line.strip() for line in result.stdout.splitlines() if line.strip()]
    assert len(tracked_files) == 0, f"Banned PoC files are still tracked by git: {tracked_files}"

    # Check .gitignore contents
    gitignore_path = repo_root / ".gitignore"
    assert gitignore_path.exists()
    gitignore_text = gitignore_path.read_text(encoding="utf-8")
    for poc in banned_pocs:
        assert poc in gitignore_text, f"{poc} is missing from .gitignore"
