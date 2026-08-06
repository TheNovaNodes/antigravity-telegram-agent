"""
MCP (Model Context Protocol) Configuration Manager for DMagyBOT.
Manages AnythingLLM (Semantic Memory), SearXNG (Web Search), and Nextcloud (User CRM).
"""

import json
import os
from pathlib import Path
from typing import Dict, Any, Optional

DEFAULT_MCP_CONFIG_PATH = Path("mcp_config.json")

DEFAULT_MCP_CONFIG = {
    "enabled": True,
    "servers": {
        "anythingllm": {
            "name": "AnythingLLM Semantic Memory",
            "type": "memory",
            "enabled": True,
            "url": os.getenv("ANYTHINGLLM_URL", "http://localhost:3001"),
            "api_key": os.getenv("ANYTHINGLLM_API_KEY", ""),
            "workspace": os.getenv("ANYTHINGLLM_WORKSPACE", "default")
        },
        "searxng": {
            "name": "SearXNG Web Search",
            "type": "search",
            "enabled": True,
            "url": os.getenv("SEARXNG_URL", "http://localhost:8080"),
            "engines": os.getenv("SEARXNG_ENGINES", "google,bing,duckduckgo")
        },
        "nextcloud": {
            "name": "Nextcloud User CRM",
            "type": "crm",
            "enabled": True,
            "url": os.getenv("NEXTCLOUD_URL", "https://cloud.example.com"),
            "username": os.getenv("NEXTCLOUD_USER", ""),
            "app_password": os.getenv("NEXTCLOUD_PASS", "")
        }
    }
}


class MCPConfigManager:
    """Manages reading, writing, and formatting MCP server configs."""

    def __init__(self, config_path: Path = DEFAULT_MCP_CONFIG_PATH):
        self.config_path = config_path
        self.config = self.load_config()

    def load_config(self) -> Dict[str, Any]:
        """Load configuration from file or fallback to environment / defaults."""
        if self.config_path.exists():
            try:
                with open(self.config_path, "r", encoding="utf-8") as f:
                    data = json.load(f)
                    # Merge with default structure to prevent missing keys
                    merged = DEFAULT_MCP_CONFIG.copy()
                    if "servers" in data:
                        for key, server in data["servers"].items():
                            if key in merged["servers"]:
                                merged["servers"][key].update(server)
                    if "enabled" in data:
                        merged["enabled"] = data["enabled"]
                    return merged
            except Exception:
                return DEFAULT_MCP_CONFIG.copy()
        return DEFAULT_MCP_CONFIG.copy()

    def save_config(self) -> bool:
        """Persist current configuration to JSON file."""
        try:
            with open(self.config_path, "w", encoding="utf-8") as f:
                json.dump(self.config, f, indent=2, ensure_ascii=False)
            return True
        except Exception:
            return False

    def get_server(self, server_key: str) -> Optional[Dict[str, Any]]:
        """Retrieve settings for a specific MCP server."""
        return self.config.get("servers", {}).get(server_key)

    def toggle_server(self, server_key: str) -> bool:
        """Toggle an MCP server on or off."""
        if server_key in self.config.get("servers", {}):
            curr = self.config["servers"][server_key].get("enabled", True)
            self.config["servers"][server_key]["enabled"] = not curr
            self.save_config()
            return not curr
        return False

    def generate_agy_mcp_settings(self) -> Dict[str, Any]:
        """
        Formats active servers into the standard MCP configuration format
        expected by Antigravity CLI (agy).
        """
        mcp_servers = {}
        servers = self.config.get("servers", {})

        # AnythingLLM Memory MCP
        if servers.get("anythingllm", {}).get("enabled"):
            cfg = servers["anythingllm"]
            mcp_servers["anythingllm-memory"] = {
                "command": "npx",
                "args": ["-y", "@anythingllm/mcp-server"],
                "env": {
                    "ANYTHINGLLM_URL": cfg.get("url", ""),
                    "ANYTHINGLLM_API_KEY": cfg.get("api_key", ""),
                    "ANYTHINGLLM_WORKSPACE": cfg.get("workspace", "")
                }
            }

        # SearXNG Web Search MCP
        if servers.get("searxng", {}).get("enabled"):
            cfg = servers["searxng"]
            mcp_servers["searxng-search"] = {
                "command": "npx",
                "args": ["-y", "searxng-mcp-server"],
                "env": {
                    "SEARXNG_URL": cfg.get("url", "")
                }
            }

        # Nextcloud CRM MCP
        if servers.get("nextcloud", {}).get("enabled"):
            cfg = servers["nextcloud"]
            mcp_servers["nextcloud-crm"] = {
                "command": "npx",
                "args": ["-y", "nextcloud-mcp-server"],
                "env": {
                    "NEXTCLOUD_URL": cfg.get("url", ""),
                    "NEXTCLOUD_USERNAME": cfg.get("username", ""),
                    "NEXTCLOUD_PASSWORD": cfg.get("app_password", "")
                }
            }

        return {"mcpServers": mcp_servers}


mcp_config = MCPConfigManager()
