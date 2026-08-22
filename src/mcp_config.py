"""
MCP (Model Context Protocol) Configuration Manager for AntigravityTelegramAgent.
Integrates custom high-performance MCP gateways from TheNovaNodes (nova-anythingllm-mcp, nova-searxng-mcp) and Nextcloud.
Supports dual-plane separation: Control Plane (Management) vs Data Plane (Operations).
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
            "name": "TheNovaNodes AnythingLLM Hybrid Gateway",
            "type": "memory",
            "plane": "data",
            "enabled": True,
            "url": os.getenv("ANYTHINGLLM_URL", "http://127.0.0.1:3002"),
            "api_key": os.getenv("ANYTHINGLLM_API_KEY", ""),
            "workspace": os.getenv("ANYTHINGLLM_WORKSPACE", "default")
        },
        "anythingllm-control": {
            "name": "TheNovaNodes AnythingLLM Control Plane",
            "type": "admin",
            "plane": "control",
            "enabled": True,
            "url": os.getenv("ANYTHINGLLM_URL", "http://127.0.0.1:3002"),
            "api_key": os.getenv("ANYTHINGLLM_API_KEY", "")
        },
        "searxng": {
            "name": "TheNovaNodes SearXNG Deep Search Gateway",
            "type": "search",
            "plane": "data",
            "enabled": True,
            "url": os.getenv("SEARXNG_URL", "http://127.0.0.1:8889"),
            "engines": os.getenv("SEARXNG_ENGINES", "google,bing,duckduckgo")
        },
        "searxng-control": {
            "name": "TheNovaNodes SearXNG Control Plane",
            "type": "admin",
            "plane": "control",
            "enabled": False,
            "url": os.getenv("SEARXNG_URL", "http://127.0.0.1:8889")
        },
        "nextcloud": {
            "name": "Nextcloud User CRM Gateway",
            "type": "crm",
            "plane": "data",
            "enabled": True,
            "url": os.getenv("NEXTCLOUD_URL", "http://127.0.0.1:8000"),
            "username": os.getenv("NEXTCLOUD_USER", ""),
            "app_password": os.getenv("NEXTCLOUD_PASS", "")
        },
        "nextcloud-control": {
            "name": "Nextcloud Admin Control Plane",
            "type": "admin",
            "plane": "control",
            "enabled": False,
            "url": os.getenv("NEXTCLOUD_URL", "http://127.0.0.1:8000"),
            "username": os.getenv("NEXTCLOUD_USER", ""),
            "app_password": os.getenv("NEXTCLOUD_PASS", "")
        },
        "google-jules-doctormes": {
            "name": "Google Jules AI Agent (Doctormes)",
            "type": "agent",
            "plane": "data",
            "enabled": True,
            "command": "/root/projects/TheNovaNodes/google-jules-mcp/.venv/bin/google-jules-mcp"
        },
        "google-jules-novanodes": {
            "name": "Google Jules AI Agent (TheNovaNodes)",
            "type": "agent",
            "plane": "data",
            "enabled": True,
            "command": "/root/projects/TheNovaNodes/google-jules-mcp/.venv/bin/google-jules-mcp"
        },
        "universal-pr-auditor": {
            "name": "Universal PR Auditor MCP",
            "type": "audit",
            "plane": "data",
            "enabled": True,
            "command": "/root/projects/TheNovaNodes/mcp-gh-pr-reviewer/run_mcp.sh"
        }
    }
}


class MCPConfigManager:
    """Manages reading, writing, and formatting MCP server configs for Control/Data Planes."""

    def __init__(self, config_path: Path = DEFAULT_MCP_CONFIG_PATH):
        self.config_path = config_path
        self.config = self.load_config()

    def load_config(self) -> Dict[str, Any]:
        """Load configuration from file or fallback to environment / defaults."""
        if self.config_path.exists():
            try:
                with open(self.config_path, "r", encoding="utf-8") as f:
                    data = json.load(f)
                    merged = DEFAULT_MCP_CONFIG.copy()
                    merged["servers"] = DEFAULT_MCP_CONFIG["servers"].copy()
                    if "servers" in data:
                        for key, server in data["servers"].items():
                            if key in merged["servers"]:
                                merged["servers"][key] = {**merged["servers"][key], **server}
                            else:
                                merged["servers"][key] = server
                    if "enabled" in data:
                        merged["enabled"] = data["enabled"]
                    return merged
            except Exception as exc:
                import logging
                logging.getLogger(__name__).error(f"Error loading MCP config from {self.config_path}: {exc}")
                return DEFAULT_MCP_CONFIG.copy()
        return DEFAULT_MCP_CONFIG.copy()

    def save_config(self) -> bool:
        """Persist current configuration to JSON file with strict 0600 permissions."""
        try:
            if self.config_path.parent:
                self.config_path.parent.mkdir(parents=True, exist_ok=True)
            with open(self.config_path, "w", encoding="utf-8") as f:
                json.dump(self.config, f, indent=2, ensure_ascii=False)
            os.chmod(self.config_path, 0o600)
            return True
        except Exception as exc:
                import logging
                logging.getLogger(__name__).error(f"Error saving MCP config to {self.config_path}: {exc}")
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

    def generate_agy_mcp_settings(self, include_control_plane: bool = False) -> Dict[str, Any]:
        """
        Formats active servers into the standard MCP configuration format.
        By default, only Data Plane (operational) tools are passed to limit context and prevent unauthorized admin actions.
        """
        mcp_servers = {}
        servers = self.config.get("servers", {})

        for key, cfg in servers.items():
            if not cfg.get("enabled"):
                continue

            plane = cfg.get("plane", "data")
            if plane == "control" and not include_control_plane:
                continue

            if key == "anythingllm":
                mcp_servers["nova-anythingllm-mcp"] = {
                    "command": "python",
                    "args": ["-m", "memory_gateway.server"],
                    "env": {
                        "MG_ALM_BASE": cfg.get("url", "http://127.0.0.1:3002/api/v1"),
                        "MG_API_KEY": cfg.get("api_key", ""),
                        "MG_WORKSPACE": cfg.get("workspace", "default")
                    }
                }
            elif key == "searxng":
                mcp_servers["nova-searxng-mcp"] = {
                    "command": "python",
                    "args": ["-m", "searxng_gateway.server"],
                    "env": {
                        "SEARXNG_URL": cfg.get("url", "http://127.0.0.1:8889")
                    }
                }
            elif key == "nextcloud":
                url = cfg.get("url", "http://127.0.0.1:8000")
                if not url.endswith("/mcp/sse"):
                    url = f"{url.rstrip('/')}/mcp/sse"
                mcp_servers["nextcloud-crm"] = {
                    "url": url
                }
            else:
                cmd = cfg.get("command")
                if not cmd and key.startswith("google-jules-"):
                    cmd = "/root/projects/TheNovaNodes/google-jules-mcp/.venv/bin/google-jules-mcp"

                if cmd:
                    env_dict = cfg.get("env", {}).copy()
                    if key.startswith("google-jules-"):
                        env_var_suffix = key.split("-")[-1].upper()
                        env_var_name = f"JULES_API_KEY_{env_var_suffix}"
                        env_val = os.environ.get(env_var_name, os.environ.get("JULES_API_KEY", ""))
                        if env_val:
                            env_dict["JULES_API_KEY"] = env_val

                    mcp_servers[key] = {
                        "command": cmd,
                        "args": cfg.get("args", []),
                        "env": env_dict
                    }
        return {"mcpServers": mcp_servers}


    def get_env_dict(self) -> Dict[str, str]:
        """Returns environment variables dictionary for child CLI processes."""
        env_dict = {}
        servers = self.config.get("servers", {})
        if "anythingllm" in servers:
            s = servers["anythingllm"]
            if s.get("api_key"):
                env_dict["ANYTHINGLLM_API_KEY"] = s["api_key"]
            if s.get("url"):
                env_dict["ANYTHINGLLM_URL"] = s["url"]
        if "searxng" in servers:
            s = servers["searxng"]
            if s.get("url"):
                env_dict["SEARXNG_URL"] = s["url"]
        return env_dict


mcp_config = MCPConfigManager()
