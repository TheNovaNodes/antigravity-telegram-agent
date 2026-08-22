"""
MCP Manager helper for checking server status and integration with AntigravityTelegramAgent.
"""

from typing import Dict, Any
from src.mcp_config import MCPConfigManager


class MCPManager:
    """Handles operational checks and UI formatting for MCP servers."""

    def __init__(self, config_manager: MCPConfigManager):
        self.config_manager = config_manager

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
                return exc.code < 500
            except Exception:
                return False

        def check_anythingllm_deep(url: str, api_key: str, timeout: float = 5.0) -> bool:
            if not api_key:
                return False
            try:
                import json
                req = urllib.request.Request(
                    f"{url.rstrip('/')}/api/v1/workspaces",
                    headers={"Authorization": f"Bearer {api_key}", "Accept": "application/json"}
                )
                with urllib.request.urlopen(req, timeout=timeout) as response:
                    data = json.loads(response.read().decode('utf-8'))
                    return "workspaces" in data or type(data) is list
            except Exception:
                return False

        def check_searxng_deep(url: str, timeout: float = 5.0) -> bool:
            try:
                import json
                req = urllib.request.Request(
                    f"{url.rstrip('/')}/search?q=test&format=json",
                    headers={"User-Agent": "AntigravityTelegramAgent/1.0"}
                )
                with urllib.request.urlopen(req, timeout=timeout) as response:
                    data = json.loads(response.read().decode('utf-8'))
                    return "results" in data
            except Exception:
                return False

        def check_command_exists(cmd: str) -> bool:
            if not cmd:
                return False
            binary = cmd.split()[0]
            return os.path.exists(binary) and os.access(binary, os.X_OK)

        async def check_mcp_binary_deep(cmd: str, timeout: float = 3.0) -> bool:
            if not check_command_exists(cmd):
                return False
            try:
                import json
                proc = await asyncio.create_subprocess_shell(
                    cmd,
                    stdin=asyncio.subprocess.PIPE,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE
                )
                init_req = {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "initialize",
                    "params": {
                        "protocolVersion": "2024-11-05",
                        "capabilities": {},
                        "clientInfo": {"name": "HealthCheck", "version": "1.0.0"}
                    }
                }
                proc.stdin.write((json.dumps(init_req) + "\n").encode('utf-8'))
                await proc.stdin.drain()
                
                response_found = False
                async def read_stdout():
                    nonlocal response_found
                    while True:
                        line = await proc.stdout.readline()
                        if not line:
                            break
                        try:
                            data = json.loads(line.decode('utf-8'))
                            if data.get("id") == 1 and "result" in data:
                                response_found = True
                                break
                        except Exception:
                            pass
                            
                try:
                    await asyncio.wait_for(read_stdout(), timeout=timeout)
                except asyncio.TimeoutError:
                    pass
                    
                proc.terminate()
                try:
                    await asyncio.wait_for(proc.wait(), timeout=1.0)
                except asyncio.TimeoutError:
                    proc.kill()
                    
                return response_found
            except Exception:
                return False

        for key, srv in servers.items():
            if not srv.get("enabled"):
                results[key] = {
                    "name": srv.get("name", key),
                    "status": "disabled",
                    "ok": False,
                    "details": "Server is disabled in config"
                }
                continue

            url = srv.get("url")
            cmd = srv.get("command")
            url_ok = True
            cmd_ok = True
            target = []

            # 1. Check HTTP Instance Health (Deep where possible)
            if url and url != "local":
                target.append(url)
                if key.startswith("anythingllm"):
                    api_key = srv.get("api_key") or srv.get("env", {}).get("ANYTHINGLLM_API_KEY")
                    if api_key:
                        url_ok = await asyncio.to_thread(check_anythingllm_deep, url, api_key)
                    else:
                        ping_url = f"{url.rstrip('/')}/api/ping"
                        url_ok = await asyncio.to_thread(check_http_url, ping_url)
                elif key.startswith("searxng"):
                    url_ok = await asyncio.to_thread(check_searxng_deep, url)
                    if not url_ok:
                        ping_url = f"{url.rstrip('/')}/healthz"
                        url_ok = await asyncio.to_thread(check_http_url, ping_url)
                elif key.startswith("nextcloud"):
                    ping_url = f"{url.rstrip('/')}/status.php"
                    url_ok = await asyncio.to_thread(check_http_url, ping_url)
                    if not url_ok:
                        url_ok = await asyncio.to_thread(check_http_url, url)
                else:
                    url_ok = await asyncio.to_thread(check_http_url, url)
            
            # 2. Check MCP Control Plane Health (Binary)
            if cmd:
                target.append(cmd.split()[0])
                if key.startswith("google-jules"):
                    cmd_ok = await check_mcp_binary_deep(cmd)
                else:
                    cmd_ok = check_command_exists(cmd)

            # 3. Combine statuses
            is_ok = False
            if url and cmd:
                is_ok = url_ok and cmd_ok
                if url_ok and cmd_ok:
                    status = "online"
                elif url_ok and not cmd_ok:
                    status = "degraded (mcp offline)"
                elif not url_ok and cmd_ok:
                    status = "degraded (instance offline)"
                else:
                    status = "offline"
            elif url and url != "local":
                is_ok = url_ok
                status = "online" if is_ok else "offline"
            elif cmd:
                is_ok = cmd_ok
                status = "online" if is_ok else "offline"
            else:
                status = "offline"

            results[key] = {
                "name": srv.get("name", key),
                "status": status,
                "ok": is_ok,
                "target": " & ".join(target) if target else "Unknown"
            }

        return results

