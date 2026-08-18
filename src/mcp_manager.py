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


mcp_manager = MCPManager()
