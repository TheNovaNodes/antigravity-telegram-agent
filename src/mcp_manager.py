"""
MCP Manager helper for checking server status and integration with DMagyBOT.
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
        global_enabled = "🟢 Включено" if cfg.get("enabled") else "🔴 Отключено"
        
        report = [
            "🧠 **Model Context Protocol (MCP) Статус**",
            f"Статус службы: {global_enabled}\n",
            "**Подключенные серверы:**"
        ]

        servers = cfg.get("servers", {})
        icons = {
            "anythingllm": "🧠",
            "searxng": "🔍",
            "nextcloud": "💼"
        }

        for key, srv in servers.items():
            icon = icons.get(key, "🔌")
            state = "✅ Активен" if srv.get("enabled") else "⚪ Отключен"
            name = srv.get("name", key)
            url = srv.get("url", "N/A")
            report.append(f"{icon} **{name}**: {state}\n   URL: `{url}`")

        return "\n".join(report)

    def toggle_server(self, server_key: str) -> bool:
        """Toggle an MCP server state."""
        return self.config_manager.toggle_server(server_key)


mcp_manager = MCPManager()
