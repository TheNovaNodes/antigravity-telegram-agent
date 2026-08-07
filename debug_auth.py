#!/usr/bin/env python3
"""Diagnostic script for DMagyBOT credential & systemd setup validation."""

import os
import sys
import json
import urllib.request
from pathlib import Path

def run_diagnostics():
    print("=" * 60)
    print("🔍 DMAGYBOT AUTH DIAGNOSTICS")
    print("=" * 60)
    
    current_user = os.getenv("USER") or "unknown"
    home_dir = Path.home()
    sudo_user = os.getenv("SUDO_USER")
    
    print(f"Current Process User : {current_user}")
    print(f"Current Process HOME : {home_dir}")
    print(f"SUDO_USER            : {sudo_user}")
    print("-" * 60)
    
    # Scan potential gemini dirs
    potential_homes = [home_dir]
    if sudo_user:
        potential_homes.append(Path(f"/home/{sudo_user}"))
    if Path("/root").exists():
        potential_homes.append(Path("/root"))
    if Path("/home").exists():
        for p in Path("/home").iterdir():
            if p.is_dir():
                potential_homes.append(p)
                
    found_tokens = []
    for h in set(potential_homes):
        token_path = h / ".gemini" / "antigravity-cli" / "antigravity-oauth-token"
        if token_path.exists():
            readable = os.access(token_path, os.R_OK)
            found_tokens.append((token_path, readable, token_path.stat().st_uid))
            
    print(f"Found OAuth Token Files ({len(found_tokens)}):")
    if not found_tokens:
        print(" ❌ NO TOKEN FILES FOUND in any home directory!")
    for tpath, readable, uid in found_tokens:
        status = "✅ READABLE" if readable else "❌ PERMISSION DENIED"
        print(f" - {tpath} (UID: {uid}, {status})")
        if readable:
            try:
                data = json.loads(tpath.read_text())
                token = data.get("token", {}).get("access_token")
                if token:
                    print(f"   Token present: {token[:10]}...")
                    # Try google userinfo
                    req = urllib.request.Request("https://www.googleapis.com/oauth2/v3/userinfo")
                    req.add_header("Authorization", f"Bearer {token}")
                    with urllib.request.urlopen(req, timeout=3.0) as resp:
                        info = json.loads(resp.read().decode())
                        print(f"   Google OAuth Email : {info.get('email')}")
                else:
                    print("   ❌ Access token field missing in JSON!")
            except Exception as e:
                print(f"   ❌ Error reading token: {e}")

    print("-" * 60)
    print("Testing src.cli_runner functions:")
    try:
        from src.cli_runner import get_active_account_email, get_auth_state_signature
        print(f"get_active_account_email()   : {get_active_account_email()}")
        print(f"get_auth_state_signature() : {get_auth_state_signature()}")
    except Exception as e:
        print(f"❌ Error invoking cli_runner functions: {e}")
        
    print("=" * 60)

if __name__ == "__main__":
    run_diagnostics()
