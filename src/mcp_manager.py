"""
MCP Manager helper for checking server status and integration with AntigravityTelegramAgent.
"""

from typing import Dict, Any
from src.mcp_config import mcp_config


class MCPManager:
    """Handles operational checks and UI formatting for MCP servers."""

    def __init__(self, manager=mcp_config):
        self.config_manager = manager

    def get_status_report(self) -> str:
        """Generates a human-readable Telegram report on MCP server statuses."""
        cfg = self.config_manager.config
        global_enabled = "🟢 Enabled" if cfg.get("enabled") else "🔴 Disabled"
        
        report = [
            "🧠 <b>Model Context Protocol (MCP) Status</b>",
            f"Service Status: {global_enabled}\n",
            "<b>Connected servers:</b>"
        ]

        servers = cfg.get("servers", {})
        icons = {
            "anythingllm": "🧠",
            "anythingllm-control": "⚙️",
            "searxng": "🔍",
            "searxng-control": "⚙️",
            "nextcloud": "💼",
            "nextcloud-control": "⚙️",
            "google-jules-doctormes": "🤖",
            "google-jules-novanodes": "🤖"
        }

        for key, srv in servers.items():
            icon = icons.get(key, "🔌")
            state = "✅ Active" if srv.get("enabled") else "⚪ Disabled"
            plane = f"[{srv.get('plane', 'data').upper()}]"
            name = srv.get("name", key)
            url_or_cmd = srv.get("url") or srv.get("command", "N/A")
            report.append(f"{icon} <b>{name}</b> {plane}: {state}\n   Target: <code>{url_or_cmd}</code>")

        return "\n".join(report)

    def toggle_server(self, server_key: str) -> bool:
        """Toggle an MCP server state."""
        return self.config_manager.toggle_server(server_key)

    async def health_check_all(self) -> Dict[str, Dict[str, Any]]:
        """Tests connectivity to AnythingLLM, SearXNG, Nextcloud, and Google Jules MCP endpoints."""
        import urllib.request
        import urllib.error
        import asyncio
        import os

        servers = self.config_manager.config.get("servers", {})
        results = {}

        def check_http_url(url: str, timeout: float = 3.0) -> bool:
            try:
                req = urllib.request.Request(url, headers={"User-Agent": "AntigravityTelegramAgent/1.0"})
                with urllib.request.urlopen(req, timeout=timeout) as response:
                    return response.status in (200, 204, 301, 302, 401, 403)
            except urllib.error.HTTPError as exc:
                # HTTP errors like 401, 403, 404 mean endpoint is reachable
                return exc.code < 500
            except Exception:
                return False

        def check_command_exists(cmd: str) -> bool:
            if not cmd:
                return False
            binary = cmd.split()[0]
            return os.path.exists(binary) and os.access(binary, os.X_OK)

        for key, srv in servers.items():
            if not srv.get("enabled"):
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "disabled",
                    "ok": False,
                    "details": "Server is disabled in config"
                }
                continue

            if key.startswith("anythingllm"):
                url = srv.get("url", "http://127.0.0.1:3002")
                ping_url = f"{url.rstrip('/')}/api/ping"
                ok = await asyncio.to_thread(check_http_url, ping_url)
                if not ok:
                    ok = await asyncio.to_thread(check_http_url, url)
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "online" if ok else "offline",
                    "ok": ok,
                    "target": url
                }
            elif key.startswith("searxng"):
                url = srv.get("url", "http://127.0.0.1:8889")
                ping_url = f"{url.rstrip('/')}/healthz"
                ok = await asyncio.to_thread(check_http_url, ping_url)
                if not ok:
                    ok = await asyncio.to_thread(check_http_url, url)
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "online" if ok else "offline",
                    "ok": ok,
                    "target": url
                }
            elif key.startswith("nextcloud"):
                url = srv.get("url", "http://127.0.0.1:8000")
                ping_url = f"{url.rstrip('/')}/status.php"
                ok = await asyncio.to_thread(check_http_url, ping_url)
                if not ok:
                    ok = await asyncio.to_thread(check_http_url, url)
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "online" if ok else "offline",
                    "ok": ok,
                    "target": url
                }
            elif key.startswith("google-jules"):
                cmd = srv.get("command", "/root/projects/TheNovaNodes/google-jules-mcp/.venv/bin/google-jules-mcp")
                ok = check_command_exists(cmd)
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "online" if ok else "offline",
                    "ok": ok,
                    "target": cmd
                }
            else:
                url_or_cmd = srv.get("url") or srv.get("command", "")
                if srv.get("url"):
                    ok = await asyncio.to_thread(check_http_url, srv["url"])
                elif srv.get("command"):
                    ok = check_command_exists(srv["command"])
                else:
                    ok = False
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "online" if ok else "offline",
                    "ok": ok,
                    "target": url_or_cmd
                }

        return results


mcp_manager = MCPManager()

